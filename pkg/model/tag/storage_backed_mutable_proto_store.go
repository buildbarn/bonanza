package tag

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_parser "bonanza.build/pkg/model/parser"
	model_initialsizeclass_pb "bonanza.build/pkg/proto/model/initialsizeclass"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/prometheus/client_golang/prometheus"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var (
	storageBackedMutableProtoHandleMetrics sync.Once

	storageBackedMutableProtoHandlesCreated = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "tag",
			Name:      "storage_backed_mutable_proto_handles_created_total",
			Help:      "Number of mutable Protobuf message handles that were created during Get()",
		})
	storageBackedMutableProtoHandlesDestroyed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "tag",
			Name:      "storage_backed_mutable_proto_handles_destroyed_total",
			Help:      "Number of mutable Protobuf message handles that were destroyed",
		})
	storageBackedMutableProtoHandlesDequeued = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "tag",
			Name:      "storage_backed_mutable_proto_handles_dequeued_total",
			Help:      "Number of mutable Protobuf message handles that were dequeued for writing during Get()",
		})

	storageBackedMutableProtoHandlesQueued = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "bonanza",
			Subsystem: "tag",
			Name:      "storage_backed_mutable_proto_handles_queued_total",
			Help:      "Number of mutable Protobuf message handles that were queued for writing",
		})
)

type storageBackedMutableProtoStore[T any, TProto interface {
	*T
	proto.Message
}] struct {
	referenceFormat        object.ReferenceFormat
	tagResolver            tag.Resolver[object.ReferenceFormat]
	tagSignaturePrivateKey ed25519.PrivateKey
	tagSignaturePublicKey  [ed25519.PublicKeySize]byte
	messageObjectReader    model_parser.MessageObjectReader[object.LocalReference, TProto]
	objectEncoder          model_encoding.KeyedBinaryEncoder
	dagUploader            dag.Uploader[struct{}, object.LocalReference]
	clock                  clock.Clock

	lock           sync.Mutex
	handles        map[[sha256.Size]byte]*storageBackedMutableProtoHandle[T, TProto]
	handlesToWrite []*storageBackedMutableProtoHandle[T, TProto]
}

// NewStorageBackedMutableProtoStore creates an instance of
// MutableProtoStore that is backed by Object Store and Tag Store.
//
// What makes this interface harder to implement is that releasing
// MutableProtoHandle is performed while holding locks. We can't block,
// nor can we propagate errors or perform retries. To solve this, this
// implementation keeps track of a list of all handles that need to be
// written. Every time a handle is created, we write a couple of
// released handles back to storage. This ensures that the number of
// handles remains proportional to actual use.
func NewStorageBackedMutableProtoStore[T any, TProto interface {
	*T
	proto.Message
}](
	referenceFormat object.ReferenceFormat,
	tagResolver tag.Resolver[object.ReferenceFormat],
	tagSignaturePrivateKey ed25519.PrivateKey,
	messageObjectReader model_parser.MessageObjectReader[object.LocalReference, TProto],
	objectEncoder model_encoding.KeyedBinaryEncoder,
	dagUploader dag.Uploader[struct{}, object.LocalReference],
	clock clock.Clock,
) MutableProtoStore[TProto] {
	storageBackedMutableProtoHandleMetrics.Do(func() {
		prometheus.MustRegister(storageBackedMutableProtoHandlesCreated)
		prometheus.MustRegister(storageBackedMutableProtoHandlesDestroyed)
		prometheus.MustRegister(storageBackedMutableProtoHandlesDequeued)
		prometheus.MustRegister(storageBackedMutableProtoHandlesQueued)
	})

	return &storageBackedMutableProtoStore[T, TProto]{
		referenceFormat:        referenceFormat,
		tagResolver:            tagResolver,
		tagSignaturePrivateKey: tagSignaturePrivateKey,
		tagSignaturePublicKey:  *(*[ed25519.PublicKeySize]byte)(tagSignaturePrivateKey.Public().(ed25519.PublicKey)),
		messageObjectReader:    messageObjectReader,
		objectEncoder:          objectEncoder,
		dagUploader:            dagUploader,
		clock:                  clock,

		handles: map[[sha256.Size]byte]*storageBackedMutableProtoHandle[T, TProto]{},
	}
}

type handleToWrite[T any, TProto interface {
	*T
	proto.Message
}] struct {
	handle         *storageBackedMutableProtoHandle[T, TProto]
	message        proto.Message
	writingVersion int
}

func (ps *storageBackedMutableProtoStore[T, TProto]) Get(ctx context.Context, tagKeyHash [sha256.Size]byte) (MutableProtoHandle[TProto], error) {
	const writesPerRead = 3
	handlesToWrite := make([]handleToWrite[T, TProto], 0, writesPerRead)

	// See if a handle for the current key hash already exists that
	// we can use. Remove it from the write queue to prevent
	// unnecessary writes to storage.
	ps.lock.Lock()
	handleToReturn, hasExistingHandle := ps.handles[tagKeyHash]
	if hasExistingHandle {
		handleToReturn.increaseUseCount()
	}

	// Extract a couple of handles from previous actions that we can
	// write to storage at this point. It is safe to access
	// handle.message here, as handle.useCount is guaranteed to be
	// zero for handles that are queued for writing.
	for i := 0; i < writesPerRead && len(ps.handlesToWrite) > 0; i++ {
		newLength := len(ps.handlesToWrite) - 1
		handle := ps.handlesToWrite[newLength]
		ps.handlesToWrite[newLength] = nil
		ps.handlesToWrite = ps.handlesToWrite[:newLength]
		if handle.handlesToWriteIndex != newLength {
			panic("Handle has bad write index")
		}
		handle.handlesToWriteIndex = -1
		handlesToWrite = append(handlesToWrite, handleToWrite[T, TProto]{
			handle:         handle,
			message:        proto.Clone(TProto(&handle.message)),
			writingVersion: handle.currentVersion,
		})
	}
	ps.lock.Unlock()
	storageBackedMutableProtoHandlesDequeued.Add(float64(len(handlesToWrite)))

	// If no handle exists, create a new handle containing the
	// existing message for the action.
	group, ctxWithCancel := errgroup.WithContext(ctx)
	if !hasExistingHandle {
		handleToReturn = &storageBackedMutableProtoHandle[T, TProto]{
			store:               ps,
			tagKeyHash:          tagKeyHash,
			useCount:            1,
			handlesToWriteIndex: -1,
		}
		group.Go(func() error {
			realTagKeyHash, decodingParameters := GetDecodingParametersFromKeyHash(ps.objectEncoder, tagKeyHash)
			if signedValue, complete, err := ps.tagResolver.ResolveTag(
				ctxWithCancel,
				ps.referenceFormat,
				tag.Key{
					SignaturePublicKey: ps.tagSignaturePublicKey,
					Hash:               realTagKeyHash,
				},
				/* minimumTimestamp = */ nil,
			); err == nil {
				if complete {
					decodableReference, err := model_core.NewDecodable(signedValue.Value.Reference, decodingParameters)
					if err != nil {
						return util.StatusWrapf(err, "Invalid decoding parameters")
					}
					if m, err := ps.messageObjectReader.ReadParsedObject(ctxWithCancel, decodableReference); err == nil {
						proto.Merge(TProto(&handleToReturn.message), m.Message)
					} else if status.Code(err) == codes.NotFound {
						return status.Errorf(
							codes.Internal,
							"Tag with key hash %s resolved to object with reference %s, which cannot be found, even though the tag was complete",
							hex.EncodeToString(tagKeyHash[:]),
							model_core.DecodableLocalReferenceToString(decodableReference),
						)
					} else {
						return util.StatusWrapf(
							err,
							"Failed to read object with reference %s used by tag with key hash %s",
							model_core.DecodableLocalReferenceToString(decodableReference),
							hex.EncodeToString(tagKeyHash[:]),
						)
					}
				}
			} else if status.Code(err) != codes.NotFound {
				return util.StatusWrapf(err, "Failed to resolve tag with key hash %s", hex.EncodeToString(tagKeyHash[:]))
			}
			return nil
		})
	}

	// Write statistics for the actions that completed previously.
	for _, handleToWriteIter := range handlesToWrite {
		handleToWrite := handleToWriteIter
		group.Go(func() error {
			tagKeyHash := handleToWrite.handle.tagKeyHash
			realTagKeyHash, decodingParameters := GetDecodingParametersFromKeyHash(ps.objectEncoder, tagKeyHash)
			if err := func() error {
				createdObject, err := model_core.MarshalAndEncodeKeyed(
					model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](
						model_core.NewProtoBinaryMarshaler(handleToWrite.message),
					),
					ps.referenceFormat,
					ps.objectEncoder,
					decodingParameters,
				)
				if err != nil {
					return util.StatusWrapf(err, "Failed to marshal and encode mutable Protobuf message for tag with key hash %s", hex.EncodeToString(tagKeyHash[:]))
				}
				rootTagValue := tag.Value{
					Reference: createdObject.Contents.GetLocalReference(),
					Timestamp: ps.clock.Now(),
				}
				rootTagSignedValue, err := rootTagValue.Sign(ps.tagSignaturePrivateKey, realTagKeyHash)
				if err != nil {
					return util.StatusWrapf(err, "Failed to sign tag with key hash %s", hex.EncodeToString(tagKeyHash[:]))
				}
				if err := ps.dagUploader.UploadTaggedDAG(
					ctxWithCancel,
					struct{}{},
					tag.Key{
						SignaturePublicKey: ps.tagSignaturePublicKey,
						Hash:               realTagKeyHash,
					},
					rootTagSignedValue,
					dag.NewSimpleObjectContentsWalker(createdObject.Contents, createdObject.Metadata),
				); err != nil {
					return util.StatusWrapf(err, "Failed to upload tag with key hash %s", hex.EncodeToString(tagKeyHash[:]))
				}
				return nil
			}(); err != nil {
				ps.lock.Lock()
				handleToWrite.handle.removeOrQueueForWriteLocked()
				ps.lock.Unlock()
				return err
			}
			ps.lock.Lock()
			handleToWrite.handle.writtenVersion = handleToWrite.writingVersion
			handleToWrite.handle.removeOrQueueForWriteLocked()
			ps.lock.Unlock()
			return nil
		})
	}

	// Wait for the read and both writes to complete.
	if err := group.Wait(); err != nil {
		ps.lock.Lock()
		if hasExistingHandle {
			handleToReturn.decreaseUseCount()
		}
		ps.lock.Unlock()
		return nil, err
	}

	if !hasExistingHandle {
		// Insert the new handle into our bookkeeping. It may be
		// the case that another thread beat us to it. Discard
		// our newly created handle in that case.
		ps.lock.Lock()
		if existingHandle, ok := ps.handles[tagKeyHash]; ok {
			handleToReturn = existingHandle
			handleToReturn.increaseUseCount()
		} else {
			ps.handles[tagKeyHash] = handleToReturn
			storageBackedMutableProtoHandlesCreated.Inc()
		}
		ps.lock.Unlock()
	}
	return handleToReturn, nil
}

type storageBackedMutableProtoHandle[T any, TProto interface {
	*T
	proto.Message
}] struct {
	store      *storageBackedMutableProtoStore[T, TProto]
	tagKeyHash [sha256.Size]byte

	// The number of times we still expect Release() to be called on
	// the handle.
	useCount int

	// The message that all users of the handle mutate. We keep a
	// version number internally to determine whether we need to
	// write the message to storage or discard it.
	message        T
	writtenVersion int
	currentVersion int

	// The index of this handle in the handlesToWrite list. We keep
	// track of this index, so that we can remove the handle from
	// the list if needed.
	handlesToWriteIndex int
}

func (ph *storageBackedMutableProtoHandle[T, TProto]) GetMutableProto() TProto {
	return TProto(&ph.message)
}

func (ph *storageBackedMutableProtoHandle[T, TProto]) increaseUseCount() {
	ph.useCount++
	if i := ph.handlesToWriteIndex; i >= 0 {
		// Handle is queued for writing. Remove it, as we'd
		// better write it after further changes have been made.
		ps := ph.store
		newLength := len(ps.handlesToWrite) - 1
		lastHandle := ps.handlesToWrite[newLength]
		ps.handlesToWrite[i] = lastHandle
		if lastHandle.handlesToWriteIndex != newLength {
			panic("Handle has bad write index")
		}
		lastHandle.handlesToWriteIndex = i
		ps.handlesToWrite[newLength] = nil
		ps.handlesToWrite = ps.handlesToWrite[:newLength]
		ph.handlesToWriteIndex = -1
		storageBackedMutableProtoHandlesDequeued.Inc()
	}
}

func (ph *storageBackedMutableProtoHandle[T, TProto]) decreaseUseCount() {
	ph.useCount--
	ph.removeOrQueueForWriteLocked()
}

func (ph *storageBackedMutableProtoHandle[T, TProto]) removeOrQueueForWriteLocked() {
	if ph.useCount == 0 {
		ps := ph.store
		if ph.writtenVersion == ph.currentVersion {
			// No changes were made to the message. Simply
			// discard this handle.
			delete(ps.handles, ph.tagKeyHash)
			storageBackedMutableProtoHandlesDestroyed.Inc()
		} else if ph.handlesToWriteIndex < 0 {
			// Changes were made and we're not queued. Place
			// handle in the queue.
			ph.handlesToWriteIndex = len(ps.handlesToWrite)
			ps.handlesToWrite = append(ps.handlesToWrite, ph)
			storageBackedMutableProtoHandlesQueued.Inc()
		}
	}
}

func (ph *storageBackedMutableProtoHandle[T, TProto]) Release(isDirty bool) {
	ps := ph.store
	ps.lock.Lock()
	defer ps.lock.Unlock()

	if isDirty {
		ph.currentVersion = ph.writtenVersion + 1
	}
	ph.decreaseUseCount()
}

type (
	DAGUploaderForTesting         dag.Uploader[struct{}, object.LocalReference]
	MessageObjectReaderForTesting model_parser.MessageObjectReader[object.LocalReference, *model_initialsizeclass_pb.PreviousExecutionStats]
	TagResolverForTesting         tag.Resolver[object.ReferenceFormat]
)
