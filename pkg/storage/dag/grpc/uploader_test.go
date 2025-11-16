package grpc_test

import (
	"context"
	"io"
	"testing"

	dag_pb "bonanza.build/pkg/proto/storage/dag"
	object_pb "bonanza.build/pkg/proto/storage/object"
	dag_grpc "bonanza.build/pkg/storage/dag/grpc"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

func TestRemoteUploader(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	client := NewMockUploaderClient(ctrl)
	uploader := dag_grpc.NewUploader(
		client,
		semaphore.NewWeighted(1),
		object.NewLimit(&object_pb.Limit{
			Count:     100,
			SizeBytes: 1 << 20,
		}),
	)

	t.Run("StreamCreationFailure", func(t *testing.T) {
		client.EXPECT().UploadDags(gomock.Any()).Return(nil, status.Error(codes.Unavailable, "Server offline"))
		rootObjectContentsWalker := NewMockObjectContentsWalker(ctrl)
		rootObjectContentsWalker.EXPECT().Discard()

		testutil.RequireEqualStatus(
			t,
			status.Error(codes.Unavailable, "Failed to create stream: Server offline"),
			uploader.UploadDAG(
				ctx,
				object.MustNewSHA256V1GlobalReference("hello/world", "f0ed77a5b3d37db998ba186a6bd6b201f432343be9e2ede174f52bd1fab343cd", 85845, 7, 1, 599552),
				rootObjectContentsWalker,
			),
		)
	})

	t.Run("HandshakeSendFailure", func(t *testing.T) {
		stream := NewMockUploader_UploadDagsClient(ctrl)
		var streamCtx context.Context
		client.EXPECT().UploadDags(gomock.Any()).
			Do(func(ctx context.Context, opts ...grpc.CallOption) { streamCtx = ctx }).
			Return(stream, nil)
		gomock.InOrder(
			stream.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_Handshake_{
					Handshake: &dag_pb.UploadDagsRequest_Handshake{
						Namespace: &object_pb.Namespace{
							InstanceName:    "hello/world",
							ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
						},
						MaximumUnfinalizedParentsLimit: &object_pb.Limit{
							Count:     100,
							SizeBytes: 1 << 20,
						},
					},
				},
			})).Return(status.Error(codes.Unavailable, "Server offline")),
			stream.EXPECT().Recv().
				Do(func() { <-streamCtx.Done() }).
				Return(nil, status.Error(codes.Unavailable, "Server offline")),
		)
		rootObjectContentsWalker := NewMockObjectContentsWalker(ctrl)
		rootObjectContentsWalker.EXPECT().Discard()

		testutil.RequireEqualStatus(
			t,
			status.Error(codes.Unavailable, "Failed to send handshake message to server: Server offline"),
			uploader.UploadDAG(
				ctx,
				object.MustNewSHA256V1GlobalReference("hello/world", "ca3d70c749a21a8a6bd60ef74b7fc988e149881e48fde520abe665d6a5995344", 85845, 7, 1, 599552),
				rootObjectContentsWalker,
			),
		)
	})

	t.Run("AlreadyExists", func(t *testing.T) {
		stream := NewMockUploader_UploadDagsClient(ctrl)
		client.EXPECT().UploadDags(gomock.Any()).Return(stream, nil)
		rootObjectContentsWalker := NewMockObjectContentsWalker(ctrl)
		rootObjectContentsWalker.EXPECT().Discard()
		gomock.InOrder(
			stream.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_Handshake_{
					Handshake: &dag_pb.UploadDagsRequest_Handshake{
						Namespace: &object_pb.Namespace{
							InstanceName:    "hello/world",
							ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
						},
						MaximumUnfinalizedParentsLimit: &object_pb.Limit{
							Count:     100,
							SizeBytes: 1 << 20,
						},
					},
				},
			})),
			stream.EXPECT().Recv().Return(&dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_Handshake_{
					Handshake: &dag_pb.UploadDagsResponse_Handshake{
						MaximumUnfinalizedDagsCount: 5,
					},
				},
			}, nil),
			stream.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_InitiateDag_{
					InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
						RootReference: []byte{
							// SHA-256 hash.
							0xca, 0x3d, 0x70, 0xc7, 0x49, 0xa2, 0x1a, 0x8a,
							0x6b, 0xd6, 0x0e, 0xf7, 0x4b, 0x7f, 0xc9, 0x88,
							0xe1, 0x49, 0x88, 0x1e, 0x48, 0xfd, 0xe5, 0x20,
							0xab, 0xe6, 0x65, 0xd6, 0xa5, 0x99, 0x53, 0x44,
							// Size in bytes.
							0x55, 0x4f, 0x01,
							// Height.
							0x07,
							// Degree.
							0x01, 0x00,
							// Maximum parents total size in bytes.
							0x26, 0x49,
						},
					},
				},
			})),
			stream.EXPECT().Recv().Return(&dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 0,
						RequestContents:      false,
					},
				},
			}, nil),
			stream.EXPECT().CloseSend(),
			stream.EXPECT().Recv().Return(&dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_FinalizeDag_{
					FinalizeDag: &dag_pb.UploadDagsResponse_FinalizeDag{
						RootReferenceIndex: 0,
					},
				},
			}, nil),
			stream.EXPECT().Recv().Return(nil, io.EOF).MinTimes(1),
		)

		require.NoError(t, uploader.UploadDAG(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "ca3d70c749a21a8a6bd60ef74b7fc988e149881e48fde520abe665d6a5995344", 85845, 7, 1, 599552),
			rootObjectContentsWalker,
		))
	})
}
