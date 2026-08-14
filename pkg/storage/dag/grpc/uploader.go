package grpc

import (
	"context"
	"io"
	"sync"

	dag_pb "bonanza.build/pkg/proto/storage/dag"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type unfinalizedObjectState struct {
	reference                  object.LocalReference
	walker                     dag.ObjectContentsWalker
	additionalReferenceIndices []uint64
}

type uploader struct {
	client                         dag_pb.UploaderClient
	objectContentsWalkerSemaphore  *semaphore.Weighted
	maximumUnfinalizedParentsLimit object.Limit
}

// NewUploader creates a DAG Uploader that transmits objects via gRPC,
// using the DAG replication protocol.
func NewUploader(client dag_pb.UploaderClient, objectContentsWalkerSemaphore *semaphore.Weighted, maximumUnfinalizedParentsLimit object.Limit) dag.Uploader[object.InstanceName, object.GlobalReference] {
	return &uploader{
		client:                         client,
		objectContentsWalkerSemaphore:  objectContentsWalkerSemaphore,
		maximumUnfinalizedParentsLimit: maximumUnfinalizedParentsLimit,
	}
}

func (u *uploader) uploadDAG(ctx context.Context, rootReference object.GlobalReference, rootObjectContentsWalker dag.ObjectContentsWalker, rootTag *dag_pb.UploadDagsRequest_InitiateDag_Tag) error {
	return program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		// State associated with all unfinalized objects. Ensure
		// that all walkers that traversed are discarded upon
		// failure.
		rootObject := &unfinalizedObjectState{
			reference: rootReference.LocalReference,
			walker:    rootObjectContentsWalker,
		}
		unfinalizedObjectsByLowestIndex := map[uint64]*unfinalizedObjectState{
			0: rootObject,
		}
		unfinalizedObjectsByReference := map[object.LocalReference]*unfinalizedObjectState{
			rootReference.LocalReference: rootObject,
		}
		dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			<-ctx.Done()
			for _, o := range unfinalizedObjectsByLowestIndex {
				if o.walker != nil {
					o.walker.Discard()
				}
			}
			return nil
		})

		stream, err := u.client.UploadDags(ctx)
		if err != nil {
			return util.StatusWrap(err, "Failed to create stream")
		}

		// Always call Recv() after we're done to ensure
		// resources associated with the stream are cleaned up.
		dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			<-ctx.Done()
			for {
				if _, err := stream.Recv(); err != nil {
					return nil
				}
			}
		})

		// Perform handshake.
		if err := stream.Send(&dag_pb.UploadDagsRequest{
			Type: &dag_pb.UploadDagsRequest_Handshake_{
				Handshake: &dag_pb.UploadDagsRequest_Handshake{
					Namespace:                      rootReference.GetNamespace().ToProto(),
					MaximumUnfinalizedParentsLimit: u.maximumUnfinalizedParentsLimit.ToProto(),
				},
			},
		}); err != nil {
			return util.StatusWrap(err, "Failed to send handshake message to server")
		}

		response, err := stream.Recv()
		if err != nil {
			return util.StatusWrap(err, "Failed to receive handshake message from server")
		}
		if _, ok := response.Type.(*dag_pb.UploadDagsResponse_Handshake_); !ok {
			return status.Error(codes.Internal, "Initial message from server did not contain a handshake")
		}

		// Initiate transmission of the DAG.
		if err := stream.Send(&dag_pb.UploadDagsRequest{
			Type: &dag_pb.UploadDagsRequest_InitiateDag_{
				InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
					RootReference: rootReference.GetRawReference(),
					RootTag:       rootTag,
				},
			},
		}); err != nil {
			return util.StatusWrap(err, "Failed to send DAG initiation message to server")
		}

		var objectsLock, sendLock sync.Mutex
		nextReferenceIndex := uint64(1)

		// Process requests for object contents.
		for {
			objectsLock.Lock()
			for len(unfinalizedObjectsByLowestIndex) == 0 {
				objectsLock.Unlock()

				// We are not going to use the stream for sending any more
				// DAGs. Close the stream for sending, so that the server
				// will hang up as well, potentially after sending a
				// FinalizeTag message.
				if err := stream.CloseSend(); err != nil {
					return util.StatusWrap(err, "Failed to close stream for sending")
				}

				if rootTag != nil {
					// After all objects have been sent, we may receive a
					// FinalizeTag message from the server, containing the
					// status.
					response, err = stream.Recv()
					if err != nil {
						return util.StatusWrap(err, "Failed to receive DAG finalization message from server")
					}
					responseTypeFinalizeTag, ok := response.Type.(*dag_pb.UploadDagsResponse_FinalizeTag_)
					if !ok {
						return status.Error(codes.Internal, "Final message from server did not contain a DAG finalization")
					}
					finalizeTag := responseTypeFinalizeTag.FinalizeTag
					if finalizeTag.RootReferenceIndex != 0 {
						return status.Errorf(codes.Internal, "Server finalized DAG with root reference index %d, which was not expected", finalizeTag.RootReferenceIndex)
					}
					if err := status.ErrorProto(finalizeTag.Status); err != nil {
						return util.StatusWrap(err, "Server failed to write tag")
					}
				}

				// Because we closed the stream for sending, the server
				// should gracefully hang up.
				if _, err := stream.Recv(); err == io.EOF {
					return nil
				} else if err != nil {
					return util.StatusWrap(err, "Failed to receive DAG finalization message from server")
				}
				return status.Error(codes.Internal, "Server sent additional messages after DAG finalization")
			}
			objectsLock.Unlock()

			response, err := stream.Recv()
			if err != nil {
				return util.StatusWrap(err, "Failed to receive message from server")
			}

			switch responseType := response.Type.(type) {
			case *dag_pb.UploadDagsResponse_RequestObjectContents_:
				requestObject := responseType.RequestObjectContents

				objectsLock.Lock()
				o, ok := unfinalizedObjectsByLowestIndex[requestObject.LowestReferenceIndex]
				if !ok {
					objectsLock.Unlock()
					return status.Errorf(codes.Internal, "Server requested object with lowest reference index %d, which was not expected", requestObject.LowestReferenceIndex)
				}
				walker := o.walker
				o.walker = nil
				objectsLock.Unlock()
				if walker == nil {
					return status.Errorf(codes.Internal, "Server requested contents of object with reference %s, even though it was already requested previously", o.reference)
				}

				if err := util.AcquireSemaphore(ctx, u.objectContentsWalkerSemaphore, 1); err != nil {
					walker.Discard()
					return err
				}
				siblingsGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
					defer u.objectContentsWalkerSemaphore.Release(1)

					contents, childrenWalkers, err := walker.GetContents(ctx)
					if err != nil {
						return util.StatusWrapf(err, "Failed to get contents of object with reference %s", o.reference)
					}

					// Assign new reference indices for all
					// children of the object. As this must
					// be done consistently with the order
					// in which the server receives
					// ProvideObjectContents messages, we
					// hold a lock across Send().
					sendLock.Lock()
					objectsLock.Lock()
					var walkersToDiscard []dag.ObjectContentsWalker
					for i, childWalker := range childrenWalkers {
						childReferenceIndex := nextReferenceIndex
						nextReferenceIndex++

						childReference := contents.GetOutgoingReference(i)
						if childObject, ok := unfinalizedObjectsByReference[childReference]; ok {
							childObject.additionalReferenceIndices = append(childObject.additionalReferenceIndices, childReferenceIndex)
							if childObject.walker == nil {
								childObject.walker = childWalker
							} else {
								walkersToDiscard = append(walkersToDiscard, childWalker)
							}
						} else {
							childObject := &unfinalizedObjectState{
								reference: childReference,
								walker:    childWalker,
							}
							unfinalizedObjectsByReference[childReference] = childObject
							unfinalizedObjectsByLowestIndex[childReferenceIndex] = childObject
						}
					}
					objectsLock.Unlock()

					err = stream.Send(&dag_pb.UploadDagsRequest{
						Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
							ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
								LowestReferenceIndex: requestObject.LowestReferenceIndex,
								ObjectContents:       contents.GetFullData(),
							},
						},
					})
					sendLock.Unlock()

					for _, childWalker := range walkersToDiscard {
						childWalker.Discard()
					}
					if err != nil {
						return util.StatusWrapf(err, "Failed to send contents of object with reference %s to server", o.reference)
					}
					return nil
				})
			case *dag_pb.UploadDagsResponse_FinalizeObject_:
				finalizeObject := responseType.FinalizeObject

				objectsLock.Lock()
				o, ok := unfinalizedObjectsByLowestIndex[finalizeObject.LowestReferenceIndex]
				if !ok {
					objectsLock.Unlock()
					return status.Errorf(codes.Internal, "Server finalized object with lowest reference index %d, which was not expected", finalizeObject.LowestReferenceIndex)
				}
				if err := status.ErrorProto(finalizeObject.Status); err != nil {
					objectsLock.Unlock()
					return util.StatusWrapf(err, "Server failed to write object with reference %s", o.reference)
				}
				delete(unfinalizedObjectsByLowestIndex, finalizeObject.LowestReferenceIndex)

				// If the DAG contains multiple outgoing
				// references pointing to the same object, the
				// server may coalesce these references and send
				// a single request. Only remove the requestable
				// object if all reference indices for the
				// object have been exhausted.
				var walker dag.ObjectContentsWalker
				if finalizeObject.AdditionalReferenceIndices > uint32(len(o.additionalReferenceIndices)) {
					objectsLock.Unlock()
					return status.Errorf(codes.Internal, "Server finalized object with lowest reference index %d and %d additional reference indices, which was not expected", finalizeObject.LowestReferenceIndex, finalizeObject.AdditionalReferenceIndices)
				} else if finalizeObject.AdditionalReferenceIndices < uint32(len(o.additionalReferenceIndices)) {
					unfinalizedObjectsByLowestIndex[o.additionalReferenceIndices[finalizeObject.AdditionalReferenceIndices]] = o
					o.additionalReferenceIndices = o.additionalReferenceIndices[finalizeObject.AdditionalReferenceIndices+1:]
				} else {
					walker = o.walker
					delete(unfinalizedObjectsByReference, o.reference)
				}
				objectsLock.Unlock()

				if walker != nil {
					walker.Discard()
				}
			default:
				return status.Error(codes.Internal, "Message from server did not contain a supported message type")
			}
		}
	})
}

func (u *uploader) UploadDAG(ctx context.Context, rootReference object.GlobalReference, rootObjectContentsWalker dag.ObjectContentsWalker) error {
	return u.uploadDAG(
		ctx,
		rootReference,
		rootObjectContentsWalker,
		/* rootTag = */ nil,
	)
}

func (u *uploader) UploadTaggedDAG(ctx context.Context, instanceName object.InstanceName, rootTagKey tag.Key, rootTagSignedValue tag.SignedValue, rootObjectContentsWalker dag.ObjectContentsWalker) error {
	rootTagKeyMessage, err := rootTagKey.ToProto()
	if err != nil {
		rootObjectContentsWalker.Discard()
		return util.StatusWrap(err, "Invalid root tag key")
	}

	return u.uploadDAG(
		ctx,
		object.GlobalReference{
			InstanceName:   instanceName,
			LocalReference: rootTagSignedValue.Value.Reference,
		},
		rootObjectContentsWalker,
		&dag_pb.UploadDagsRequest_InitiateDag_Tag{
			Key:       rootTagKeyMessage,
			Timestamp: timestamppb.New(rootTagSignedValue.Value.Timestamp),
			Signature: rootTagSignedValue.Signature[:],
		},
	)
}
