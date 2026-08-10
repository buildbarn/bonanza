package buffered_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"slices"
	"testing"
	"time"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/buffered"
	dag_pb "bonanza.build/pkg/proto/storage/dag"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/eviction"
	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpc_status "google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

var testNamespace = object.Namespace{
	InstanceName:    util.Must(object.NewInstanceName("hello")),
	ReferenceFormat: object.SHA256V1ReferenceFormat,
}

// objectPoolHarness holds an ObjectPool for which SendQueuedObjects()
// is running in the background. Tests act as the server by reading
// requests and injecting responses through channels.
//
// As the pool calls Send() and Recv() from separate goroutines, the
// mocked stream is backed by channels, as opposed to having tests set
// up an ordered list of expectations. This ensures that Recv() blocks
// until the test provides the next response.
type objectPoolHarness struct {
	t             *testing.T
	pool          *buffered.ObjectPool
	objectManager model_core.ObjectManager[buffered.Reference, buffered.ReferenceMetadata]
	requests      chan *dag_pb.UploadDagsRequest
	responses     chan *dag_pb.UploadDagsResponse
	cancel        context.CancelFunc
	errChan       chan error
}

// newObjectPoolHarness creates an ObjectPool and calls
// SendQueuedObjects(). The handshake is performed after
// beforeHandshake is called. This allows tests to capture objects
// before any of them are sent, making the order in which DAGs are
// initiated deterministic.
func newObjectPoolHarness(t *testing.T, ctrl *gomock.Controller, limit object.Limit, maximumUnfinalizedDAGsCount uint32, beforeHandshake func(h *objectPoolHarness)) *objectPoolHarness {
	pool := buffered.NewObjectPool(limit, eviction.NewLRUSet[object.LocalReference]())
	h := &objectPoolHarness{
		t:             t,
		pool:          pool,
		objectManager: buffered.NewObjectManager(pool),
		requests:      make(chan *dag_pb.UploadDagsRequest),
		responses:     make(chan *dag_pb.UploadDagsResponse, 100),
		errChan:       make(chan error, 1),
	}

	stream := NewMockUploader_UploadDagsClient(ctrl)
	var streamCtx context.Context
	client := NewMockUploaderClient(ctrl)
	client.EXPECT().UploadDags(gomock.Any()).DoAndReturn(
		func(ctx context.Context, opts ...grpc.CallOption) (dag_pb.Uploader_UploadDagsClient, error) {
			streamCtx = ctx
			return stream, nil
		},
	)
	stream.EXPECT().Send(gomock.Any()).DoAndReturn(
		func(request *dag_pb.UploadDagsRequest) error {
			select {
			case h.requests <- request:
				return nil
			case <-streamCtx.Done():
				return util.StatusFromContext(streamCtx)
			}
		},
	).AnyTimes()
	stream.EXPECT().Recv().DoAndReturn(
		func() (*dag_pb.UploadDagsResponse, error) {
			select {
			case response := <-h.responses:
				return response, nil
			case <-streamCtx.Done():
				return nil, util.StatusFromContext(streamCtx)
			}
		},
	).AnyTimes()

	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		h.errChan <- pool.SendQueuedObjects(ctx, client, testNamespace)
	}()

	testutil.RequireEqualProto(t, &dag_pb.UploadDagsRequest{
		Type: &dag_pb.UploadDagsRequest_Handshake_{
			Handshake: &dag_pb.UploadDagsRequest_Handshake{
				Namespace: &object_pb.Namespace{
					InstanceName:    "hello",
					ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
				},
			},
		},
	}, h.expectRequest())

	if beforeHandshake != nil {
		beforeHandshake(h)
	}

	h.respond(&dag_pb.UploadDagsResponse{
		Type: &dag_pb.UploadDagsResponse_Handshake_{
			Handshake: &dag_pb.UploadDagsResponse_Handshake{
				MaximumUnfinalizedDagsCount: maximumUnfinalizedDAGsCount,
			},
		},
	})
	return h
}

func (h *objectPoolHarness) capture(contents *object.Contents) buffered.Reference {
	metadataEntry, err := model_core.CreatedObject[buffered.ReferenceMetadata]{
		Contents: contents,
	}.Capture(context.Background(), h.objectManager)
	require.NoError(h.t, err)
	return h.objectManager.ReferenceObject(metadataEntry)
}

func (h *objectPoolHarness) expectRequest() *dag_pb.UploadDagsRequest {
	select {
	case request := <-h.requests:
		return request
	case <-time.After(10 * time.Second):
		h.t.Fatal("Timed out waiting for request")
		return nil
	}
}

func (h *objectPoolHarness) expectNoRequest() {
	select {
	case request := <-h.requests:
		h.t.Fatalf("Received unexpected request: %v", request)
	case <-time.After(100 * time.Millisecond):
	}
}

func (h *objectPoolHarness) expectInitiateDAG(rootReference object.LocalReference) {
	testutil.RequireEqualProto(h.t, &dag_pb.UploadDagsRequest{
		Type: &dag_pb.UploadDagsRequest_InitiateDag_{
			InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
				RootReference: rootReference.GetRawReference(),
			},
		},
	}, h.expectRequest())
}

func (h *objectPoolHarness) requestAndExpectContents(lowestReferenceIndex uint64, contents *object.Contents) {
	h.respond(&dag_pb.UploadDagsResponse{
		Type: &dag_pb.UploadDagsResponse_RequestObjectContents_{
			RequestObjectContents: &dag_pb.UploadDagsResponse_RequestObjectContents{
				LowestReferenceIndex: lowestReferenceIndex,
			},
		},
	})
	testutil.RequireEqualProto(h.t, &dag_pb.UploadDagsRequest{
		Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
			ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
				LowestReferenceIndex: lowestReferenceIndex,
				ObjectContents:       contents.GetFullData(),
			},
		},
	}, h.expectRequest())
}

func (h *objectPoolHarness) finalizeObject(lowestReferenceIndex uint64, additionalReferenceIndices uint32, s *status.Status) {
	h.respond(&dag_pb.UploadDagsResponse{
		Type: &dag_pb.UploadDagsResponse_FinalizeObject_{
			FinalizeObject: &dag_pb.UploadDagsResponse_FinalizeObject{
				LowestReferenceIndex:       lowestReferenceIndex,
				AdditionalReferenceIndices: additionalReferenceIndices,
				Status:                     s,
			},
		},
	})
}

func (h *objectPoolHarness) respond(response *dag_pb.UploadDagsResponse) {
	h.responses <- response
}

func (h *objectPoolHarness) requireWritten(reference buffered.Reference) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exportedReference, err := buffered.ObjectExporter.ExportReference(ctx, reference)
	require.NoError(h.t, err)
	require.Equal(h.t, reference.LocalReference, exportedReference)
}

func (h *objectPoolHarness) requireNotWritten(reference buffered.Reference) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := buffered.ObjectExporter.ExportReference(ctx, reference)
	require.Equal(h.t, codes.DeadlineExceeded, grpc_status.Code(err))
}

// close terminates SendQueuedObjects() gracefully.
func (h *objectPoolHarness) close() {
	h.cancel()
	require.NoError(h.t, <-h.errChan)
}

func mustNewContents(references []object.LocalReference, payload string) *object.Contents {
	// Outgoing references of objects need to be sorted.
	references = slices.Clone(references)
	slices.SortFunc(references, func(a, b object.LocalReference) int {
		return bytes.Compare(a.GetRawReference(), b.GetRawReference())
	})
	return object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, references, []byte(payload))
}

func TestObjectPool(t *testing.T) {
	ctrl := gomock.NewController(t)

	leaf1 := mustNewContents(nil, "Leaf 1")
	leaf2 := mustNewContents(nil, "Leaf 2")
	parent := mustNewContents([]object.LocalReference{leaf1.LocalReference, leaf2.LocalReference}, "Parent")

	t.Run("SingleObject", func(t *testing.T) {
		// Capturing an object should cause it to be uploaded.
		// While uploading, it should be readable from the pool.
		var reference buffered.Reference
		baseObjectReader := NewMockObjectReaderForTesting(ctrl)
		h := newObjectPoolHarness(t, ctrl, object.Unlimited, 10, func(h *objectPoolHarness) {
			reference = h.capture(leaf1)
		})
		objectReader := buffered.NewObjectReader(baseObjectReader, h.pool)

		m, err := objectReader.ReadObject(context.Background(), reference)
		require.NoError(t, err)
		require.Equal(t, []byte("Leaf 1"), m.Message)

		h.expectInitiateDAG(leaf1.LocalReference)
		h.requestAndExpectContents(0, leaf1)
		h.requireNotWritten(reference)
		h.finalizeObject(0, 0, nil)
		h.requireWritten(reference)

		// After writing, the object remains cached.
		m, err = objectReader.ReadObject(context.Background(), reference)
		require.NoError(t, err)
		require.Equal(t, []byte("Leaf 1"), m.Message)

		h.expectNoRequest()
		h.close()
	})

	t.Run("ParentCoversQueuedChildren", func(t *testing.T) {
		// If uploading can't keep up, multiple objects will be
		// queued. The highest object should be used as the
		// root of the DAG. Its children should be removed from
		// the queue as soon as they are assigned a reference
		// index.
		var leaf1Reference, leaf2Reference, parentReference buffered.Reference
		h := newObjectPoolHarness(t, ctrl, object.Unlimited, 1, func(h *objectPoolHarness) {
			leaf1Reference = h.capture(leaf1)
			leaf2Reference = h.capture(leaf2)
			parentReference = h.capture(parent)
		})

		h.expectInitiateDAG(parent.LocalReference)
		h.requestAndExpectContents(0, parent)
		h.expectNoRequest()

		// Children are assigned reference indices in the
		// order in which they appear in the parent.
		firstChild, firstChildReference := leaf1, leaf1Reference
		secondChild, secondChildReference := leaf2, leaf2Reference
		if parent.GetOutgoingReference(0) != leaf1.LocalReference {
			firstChild, firstChildReference = leaf2, leaf2Reference
			secondChild, secondChildReference = leaf1, leaf1Reference
		}
		h.requestAndExpectContents(1, firstChild)
		h.requestAndExpectContents(2, secondChild)
		h.finalizeObject(1, 0, nil)
		h.requireWritten(firstChildReference)
		h.requireNotWritten(parentReference)
		h.finalizeObject(2, 0, nil)
		h.requireWritten(secondChildReference)
		h.finalizeObject(0, 0, nil)
		h.requireWritten(parentReference)

		// The children should not be uploaded separately.
		h.expectNoRequest()
		h.close()
	})

	t.Run("ParentAlreadyExists", func(t *testing.T) {
		// If the server reports that the root of a DAG already
		// exists, its children are also known to exist. Any
		// queued children should be marked as written, without
		// initiating separate DAGs for them.
		var leaf1Reference, leaf2Reference, parentReference buffered.Reference
		h := newObjectPoolHarness(t, ctrl, object.Unlimited, 1, func(h *objectPoolHarness) {
			leaf1Reference = h.capture(leaf1)
			leaf2Reference = h.capture(leaf2)
			parentReference = h.capture(parent)
		})

		h.expectInitiateDAG(parent.LocalReference)
		h.finalizeObject(0, 0, nil)
		h.requireWritten(parentReference)
		h.requireWritten(leaf1Reference)
		h.requireWritten(leaf2Reference)

		h.expectNoRequest()
		h.close()
	})

	t.Run("CollapsedReferenceIndices", func(t *testing.T) {
		// Two parents share the same child. The server may
		// acknowledge both reference indices of the child
		// using a single FinalizeObject message.
		parent1 := mustNewContents([]object.LocalReference{leaf1.LocalReference}, "Parent 1")
		parent2 := mustNewContents([]object.LocalReference{leaf1.LocalReference}, "Parent 2")
		first, second := parent1, parent2
		if first.LocalReference.CompareByHeight(second.LocalReference) < 0 {
			first, second = second, first
		}

		var leaf1Reference, firstReference, secondReference buffered.Reference
		h := newObjectPoolHarness(t, ctrl, object.Unlimited, 2, func(h *objectPoolHarness) {
			leaf1Reference = h.capture(leaf1)
			firstReference = h.capture(first)
			secondReference = h.capture(second)
		})

		h.expectInitiateDAG(first.LocalReference)
		h.expectInitiateDAG(second.LocalReference)
		h.requestAndExpectContents(0, first)
		h.requestAndExpectContents(1, second)
		h.requestAndExpectContents(2, leaf1)
		h.finalizeObject(2, 1, nil)
		h.requireWritten(leaf1Reference)
		h.finalizeObject(0, 0, nil)
		h.finalizeObject(1, 0, nil)
		h.requireWritten(firstReference)
		h.requireWritten(secondReference)

		h.expectNoRequest()
		h.close()
	})

	t.Run("PartiallyCollapsedReferenceIndices", func(t *testing.T) {
		// If the server acknowledges fewer reference indices
		// than known, it should still be able to refer to the
		// remaining ones by their lowest index.
		parent1 := mustNewContents([]object.LocalReference{leaf1.LocalReference}, "Parent 1")
		parent2 := mustNewContents([]object.LocalReference{leaf1.LocalReference}, "Parent 2")
		first, second := parent1, parent2
		if first.LocalReference.CompareByHeight(second.LocalReference) < 0 {
			first, second = second, first
		}

		h := newObjectPoolHarness(t, ctrl, object.Unlimited, 2, func(h *objectPoolHarness) {
			h.capture(leaf1)
			h.capture(first)
			h.capture(second)
		})

		h.expectInitiateDAG(first.LocalReference)
		h.expectInitiateDAG(second.LocalReference)
		h.requestAndExpectContents(0, first)
		h.requestAndExpectContents(1, second)
		h.finalizeObject(2, 0, nil)
		h.finalizeObject(3, 0, nil)
		h.finalizeObject(0, 0, nil)
		h.finalizeObject(1, 0, nil)

		// Sending a FinalizeObject for an index that has
		// already been finalized should cause a failure.
		h.finalizeObject(3, 0, nil)
		require.Equal(t, codes.Internal, grpc_status.Code(<-h.errChan))
	})

	t.Run("FailedFinalizeObject", func(t *testing.T) {
		// If the server fails to write an object,
		// SendQueuedObjects() should return an error. Objects
		// that have not been written should remain unwritten,
		// and the pool should no longer accept objects.
		var reference buffered.Reference
		h := newObjectPoolHarness(t, ctrl, object.Unlimited, 10, func(h *objectPoolHarness) {
			reference = h.capture(leaf1)
		})

		h.expectInitiateDAG(leaf1.LocalReference)
		h.finalizeObject(0, 0, grpc_status.New(codes.Unavailable, "Storage offline").Proto())
		err := <-h.errChan
		require.Equal(t, codes.Unavailable, grpc_status.Code(err))
		require.Contains(t, err.Error(), "Storage offline")

		h.requireNotWritten(reference)

		_, err = model_core.CreatedObject[buffered.ReferenceMetadata]{
			Contents: leaf2,
		}.Capture(context.Background(), h.objectManager)
		require.Equal(t, codes.Unavailable, grpc_status.Code(err))
	})

	t.Run("CaptureLimitAndEviction", func(t *testing.T) {
		// If the pool is full and none of the objects have
		// been written, capturing should block. Once objects
		// have been written, they can be evicted.
		baseObjectReader := NewMockObjectReaderForTesting(ctrl)
		var leaf1Reference buffered.Reference
		h := newObjectPoolHarness(t, ctrl, object.NewLimit(&object_pb.Limit{
			Count:     1,
			SizeBytes: 1000,
		}), 10, func(h *objectPoolHarness) {
			leaf1Reference = h.capture(leaf1)
		})
		objectReader := buffered.NewObjectReader(baseObjectReader, h.pool)

		h.expectInitiateDAG(leaf1.LocalReference)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, err := model_core.CreatedObject[buffered.ReferenceMetadata]{
			Contents: leaf2,
		}.Capture(ctx, h.objectManager)
		cancel()
		require.Equal(t, codes.DeadlineExceeded, grpc_status.Code(err))

		h.finalizeObject(0, 0, nil)
		h.requireWritten(leaf1Reference)

		// Capturing leaf2 should now succeed, evicting leaf1.
		leaf2Reference := h.capture(leaf2)
		h.expectInitiateDAG(leaf2.LocalReference)

		// Reading leaf1 should now go to storage.
		baseObjectReader.EXPECT().ReadObject(gomock.Any(), leaf1.LocalReference).
			Return(model_core.NewMessage([]byte("Leaf 1"), object.OutgoingReferencesList[object.LocalReference]{}), nil)
		m, err := objectReader.ReadObject(context.Background(), leaf1Reference)
		require.NoError(t, err)
		require.Equal(t, []byte("Leaf 1"), m.Message)

		h.requestAndExpectContents(1, leaf2)
		h.finalizeObject(1, 0, nil)
		h.requireWritten(leaf2Reference)
		h.close()
	})

	t.Run("EvictionSetTouching", func(t *testing.T) {
		// Reading an object that has been written to storage
		// should touch it in the eviction set. With LRU, this
		// causes another object to be evicted first.
		baseObjectReader := NewMockObjectReaderForTesting(ctrl)
		first, second := leaf1, leaf2
		if first.LocalReference.CompareByHeight(second.LocalReference) < 0 {
			first, second = second, first
		}
		var firstReference, secondReference buffered.Reference
		h := newObjectPoolHarness(t, ctrl, object.NewLimit(&object_pb.Limit{
			Count:     2,
			SizeBytes: 1000,
		}), 1, func(h *objectPoolHarness) {
			firstReference = h.capture(first)
			secondReference = h.capture(second)
		})
		objectReader := buffered.NewObjectReader(baseObjectReader, h.pool)

		h.expectInitiateDAG(first.LocalReference)
		h.finalizeObject(0, 0, nil)
		h.requireWritten(firstReference)
		h.expectInitiateDAG(second.LocalReference)
		h.finalizeObject(1, 0, nil)
		h.requireWritten(secondReference)

		// Touch the object that was written first.
		_, err := objectReader.ReadObject(context.Background(), firstReference)
		require.NoError(t, err)

		// Capturing another object should evict the object
		// that was written second.
		leaf3 := mustNewContents(nil, "Leaf 3")
		h.capture(leaf3)
		h.expectInitiateDAG(leaf3.LocalReference)

		_, err = objectReader.ReadObject(context.Background(), firstReference)
		require.NoError(t, err)
		baseObjectReader.EXPECT().ReadObject(gomock.Any(), second.LocalReference).
			Return(model_core.NewMessage(second.GetPayload(), object.OutgoingReferencesList[object.LocalReference]{}), nil)
		_, err = objectReader.ReadObject(context.Background(), secondReference)
		require.NoError(t, err)
		h.close()
	})

	t.Run("ObjectTooLarge", func(t *testing.T) {
		pool := buffered.NewObjectPool(object.NewLimit(&object_pb.Limit{
			Count:     10,
			SizeBytes: 3,
		}), eviction.NewLRUSet[object.LocalReference]())
		_, err := model_core.CreatedObject[buffered.ReferenceMetadata]{
			Contents: leaf1,
		}.Capture(context.Background(), buffered.NewObjectManager(pool))
		require.Equal(t, codes.InvalidArgument, grpc_status.Code(err))
	})

	t.Run("UpdateTag", func(t *testing.T) {
		// Tags should be created asynchronously through the
		// same stream. FlushTags() should only complete after
		// receiving FinalizeTag.
		h := newObjectPoolHarness(t, ctrl, object.Unlimited, 10, nil)
		privateKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
		tagUpdater := buffered.NewTagBoundUpdater(h.pool, privateKey, clock.SystemClock)

		require.NoError(t, h.pool.FlushTags(context.Background()))

		var keyHash [32]byte
		keyHash[0] = 42
		require.NoError(t, tagUpdater.UpdateTag(context.Background(), keyHash, buffered.Reference{
			LocalReference: leaf1.LocalReference,
		}))

		request := h.expectRequest()
		initiateDAG := request.GetInitiateDag()
		require.NotNil(t, initiateDAG)
		require.Equal(t, leaf1.LocalReference.GetRawReference(), initiateDAG.RootReference)
		require.Equal(t, keyHash[:], initiateDAG.RootTag.GetKey().GetHash())

		errChan := make(chan error, 1)
		go func() {
			errChan <- h.pool.FlushTags(context.Background())
		}()

		h.finalizeObject(0, 0, nil)
		select {
		case err := <-errChan:
			t.Fatalf("FlushTags() returned before FinalizeTag was received: %v", err)
		case <-time.After(10 * time.Millisecond):
		}

		h.respond(&dag_pb.UploadDagsResponse{
			Type: &dag_pb.UploadDagsResponse_FinalizeTag_{
				FinalizeTag: &dag_pb.UploadDagsResponse_FinalizeTag{
					RootReferenceIndex: 0,
				},
			},
		})
		require.NoError(t, <-errChan)
		h.close()
	})

	t.Run("FailedFinalizeTag", func(t *testing.T) {
		// Failures to create tags should be reported by
		// SendQueuedObjects(), and cause FlushTags() to fail.
		h := newObjectPoolHarness(t, ctrl, object.Unlimited, 10, nil)
		privateKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
		tagUpdater := buffered.NewTagBoundUpdater(h.pool, privateKey, clock.SystemClock)

		require.NoError(t, tagUpdater.UpdateTag(context.Background(), [32]byte{}, buffered.Reference{
			LocalReference: leaf1.LocalReference,
		}))
		require.NotNil(t, h.expectRequest().GetInitiateDag())

		h.finalizeObject(0, 0, nil)
		h.respond(&dag_pb.UploadDagsResponse{
			Type: &dag_pb.UploadDagsResponse_FinalizeTag_{
				FinalizeTag: &dag_pb.UploadDagsResponse_FinalizeTag{
					RootReferenceIndex: 0,
					Status:             grpc_status.New(codes.PermissionDenied, "Invalid signature").Proto(),
				},
			},
		})

		err := <-h.errChan
		require.Equal(t, codes.PermissionDenied, grpc_status.Code(err))
		require.Contains(t, err.Error(), "Invalid signature")

		require.Equal(t, codes.PermissionDenied, grpc_status.Code(h.pool.FlushTags(context.Background())))
		require.Equal(t, codes.PermissionDenied, grpc_status.Code(tagUpdater.UpdateTag(context.Background(), [32]byte{}, buffered.Reference{
			LocalReference: leaf1.LocalReference,
		})))
	})
}
