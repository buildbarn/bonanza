package grpc

import (
	"context"
	"io"
	"sync"

	dag_pb "bonanza.build/pkg/proto/storage/dag"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"
	pg_sync "bonanza.build/pkg/sync"

	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type requestableObjectState struct {
	reference                  object.LocalReference
	walker                     dag.ObjectContentsWalker
	additionalReferenceIndices []uint64
}

type uploader struct {
	client                         dag_pb.UploaderClient
	objectContentsWalkerSemaphore  *semaphore.Weighted
	maximumUnfinalizedParentsLimit object.Limit
}

func NewUploader(client dag_pb.UploaderClient, objectContentsWalkerSemaphore *semaphore.Weighted, maximumUnfinalizedParentsLimit object.Limit) dag.Uploader[object.GlobalReference] {
	return &uploader{
		client:                         client,
		objectContentsWalkerSemaphore:  objectContentsWalkerSemaphore,
		maximumUnfinalizedParentsLimit: maximumUnfinalizedParentsLimit,
	}
}

// UploadDAG uploads a single DAG of objects to a server via gRPC.
func (u *uploader) UploadDAG(ctx context.Context, rootReference object.GlobalReference, rootObjectContentsWalker dag.ObjectContentsWalker) error {
	return program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		// State associated with all requestable objects. Ensure
		// that all walkers that traversed are discarded upon
		// failure.
		rootObject := &requestableObjectState{
			reference: rootReference.LocalReference,
			walker:    rootObjectContentsWalker,
		}
		requestableObjectsByLowestIndex := map[uint64]*requestableObjectState{
			0: rootObject,
		}
		requestableObjectsByReference := map[object.LocalReference]*requestableObjectState{
			rootReference.LocalReference: rootObject,
		}
		dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			<-ctx.Done()
			for _, o := range requestableObjectsByLowestIndex {
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
				},
			},
		}); err != nil {
			return util.StatusWrap(err, "Failed to send DAG initiation message to server")
		}

		var objectsLock, sendLock sync.Mutex
		nextReferenceIndex := uint64(1)
		currentlyRequestedObjectsCount := 0
		var receiveWakeup pg_sync.ConditionVariable

		// Process requests for object contents.
		for {
			objectsLock.Lock()
			for len(requestableObjectsByLowestIndex) == 0 {
				if currentlyRequestedObjectsCount == 0 {
					objectsLock.Unlock()

					// We are not going to use the stream for sending any
					// more DAGs. Close the stream for sending, so that the
					// server will hang up as well after sending FinalizeDag.
					if err := stream.CloseSend(); err != nil {
						return util.StatusWrap(err, "Failed to close stream for sending")
					}

					// After all objects have been sent, we should receive a
					// FinalizeDag message from the server, containing the
					// status.
					response, err = stream.Recv()
					if err != nil {
						return util.StatusWrap(err, "Failed to receive DAG finalization message from server")
					}
					responseTypeFinalizeDAG, ok := response.Type.(*dag_pb.UploadDagsResponse_FinalizeDag_)
					if !ok {
						return status.Error(codes.Internal, "Final message from server did not contain a DAG finalization")
					}
					finalizeDAG := responseTypeFinalizeDAG.FinalizeDag
					if finalizeDAG.RootReferenceIndex != 0 {
						return status.Errorf(codes.Internal, "Server finalized DAG with root reference index %d, which was not expected", finalizeDAG.RootReferenceIndex)
					}
					if err := status.ErrorProto(finalizeDAG.Status); err != nil {
						return err
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

				if err := receiveWakeup.Wait(ctx, &objectsLock); err != nil {
					return err
				}
			}
			objectsLock.Unlock()

			response, err := stream.Recv()
			if err != nil {
				return util.StatusWrap(err, "Failed to receive object request message from server")
			}
			responseTypeRequestObject, ok := response.Type.(*dag_pb.UploadDagsResponse_RequestObject_)
			if !ok {
				return status.Error(codes.Internal, "Message from server did not contain an object request")
			}
			requestObject := responseTypeRequestObject.RequestObject

			objectsLock.Lock()
			o, ok := requestableObjectsByLowestIndex[requestObject.LowestReferenceIndex]
			if !ok {
				objectsLock.Unlock()
				return status.Errorf(codes.Internal, "Server requested object with lowest reference index %d, which was not expected", requestObject.LowestReferenceIndex)
			}
			delete(requestableObjectsByLowestIndex, requestObject.LowestReferenceIndex)

			// If the DAG contains multiple outgoing
			// references pointing to the same object, the
			// server may coalesce these references and send
			// a single request. Only remove the requestable
			// object if all reference indices for the
			// object have been exhausted.
			if requestObject.AdditionalReferenceIndices > uint32(len(o.additionalReferenceIndices)) {
				objectsLock.Unlock()
				return status.Errorf(codes.Internal, "Server requested object with lowest reference index %d, which was not expected", requestObject.LowestReferenceIndex)
			} else if requestObject.AdditionalReferenceIndices < uint32(len(o.additionalReferenceIndices)) {
				requestableObjectsByLowestIndex[o.additionalReferenceIndices[requestObject.AdditionalReferenceIndices]] = o
				o.additionalReferenceIndices = o.additionalReferenceIndices[requestObject.AdditionalReferenceIndices+1:]
			} else {
				delete(requestableObjectsByReference, o.reference)
			}

			// Detach the walker, because we might receive
			// other RequestObject messages for the same
			// object while processing.
			walker := o.walker
			o.walker = nil
			if requestObject.RequestContents {
				currentlyRequestedObjectsCount++
			}
			objectsLock.Unlock()

			if err := util.AcquireSemaphore(ctx, u.objectContentsWalkerSemaphore, 1); err != nil {
				if walker != nil {
					walker.Discard()
				}
				return err
			}
			siblingsGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				defer u.objectContentsWalkerSemaphore.Release(1)

				if requestObject.RequestContents {
					if walker == nil {
						return status.Errorf(codes.Internal, "Server requested contents of object with reference %s, even though it was already requested previously", o.reference)
					}
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
						if oChild, ok := requestableObjectsByReference[childReference]; ok {
							oChild.additionalReferenceIndices = append(oChild.additionalReferenceIndices, childReferenceIndex)
							walkersToDiscard = append(walkersToDiscard, childWalker)
						} else {
							childObject := &requestableObjectState{
								reference: childReference,
								walker:    childWalker,
							}
							requestableObjectsByReference[childReference] = childObject
							requestableObjectsByLowestIndex[childReferenceIndex] = childObject
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

					// Now that the response has been sent,
					// permit the main goroutine to call
					// CloseSend() if no more work remains.
					objectsLock.Lock()
					currentlyRequestedObjectsCount--
					receiveWakeup.Broadcast()
					objectsLock.Unlock()

					for _, childWalker := range walkersToDiscard {
						childWalker.Discard()
					}
					if err != nil {
						return util.StatusWrapf(err, "Failed to send contents of object with reference %s to server", o.reference)
					}
				} else if walker != nil {
					walker.Discard()
				}
				return nil
			})
		}
	})
}
