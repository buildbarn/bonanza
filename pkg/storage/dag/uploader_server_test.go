package dag_test

import (
	"context"
	"io"
	"testing"
	"time"

	dag_pb "bonanza.build/pkg/proto/storage/dag"
	object_pb "bonanza.build/pkg/proto/storage/object"
	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.uber.org/mock/gomock"
)

func TestUploaderServer(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	objectUploader := NewMockObjectUploader(ctrl)
	tagUpdater := NewMockTagUpdater(ctrl)
	uploaderServer := dag.NewUploaderServer(
		objectUploader,
		/* objectStoreSemaphore = */ semaphore.NewWeighted(1),
		tagUpdater,
		/* maximumUnfinalizedDAGsCount = */ 5,
		object.NewLimit(&object_pb.Limit{
			Count:     10,
			SizeBytes: 10000,
		}),
	)

	t.Run("NoInitialMessage", func(t *testing.T) {
		// The client should at least send a single message.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Recv().Return(nil, io.EOF)

		require.Equal(t, status.Error(codes.InvalidArgument, "Did not receive initial message from client"), uploaderServer.UploadDags(server))
	})

	t.Run("NoHandshakeInInitialMessage", func(t *testing.T) {
		// The initial message sent by the client should always
		// be a handshake.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
			Type: &dag_pb.UploadDagsRequest_InitiateDag_{
				InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
					RootReference: []byte{
						// SHA-256 hash.
						0x67, 0x8d, 0xb6, 0x9b, 0xba, 0x11, 0x39, 0x19,
						0xe2, 0x10, 0xfd, 0xac, 0x6e, 0x1f, 0xdb, 0x8d,
						0xed, 0x28, 0x4d, 0xdd, 0xe6, 0xc1, 0x43, 0x33,
						0x60, 0x62, 0xaf, 0x14, 0x65, 0xfe, 0xcb, 0xbf,
						// Size in bytes.
						0x95, 0x28, 0x00,
						// Height.
						0x00,
						// Degree.
						0x00, 0x00,
						// Maximum parents total size in bytes.
						0x00, 0x00,
					},
				},
			},
		}, nil)

		testutil.RequireEqualStatus(
			t,
			status.Error(codes.InvalidArgument, "Initial message from client did not contain a handshake"),
			uploaderServer.UploadDags(server),
		)
	})

	t.Run("HandshakeMissingNamespace", func(t *testing.T) {
		// The handhshake message should contain a namespace.
		// Otherwise we don't know the instance name and
		// reference format being used.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
			Type: &dag_pb.UploadDagsRequest_Handshake_{
				Handshake: &dag_pb.UploadDagsRequest_Handshake{
					MaximumUnfinalizedParentsLimit: &object_pb.Limit{
						Count:     100,
						SizeBytes: 1 << 20,
					},
				},
			},
		}, nil)

		testutil.RequireEqualStatus(
			t,
			status.Error(codes.InvalidArgument, "Invalid namespace: No message provided"),
			uploaderServer.UploadDags(server),
		)
	})

	t.Run("NoOp", func(t *testing.T) {
		// It's perfectly valid for a client to perform a
		// handshake, but eventually decide not to upload any
		// DAGs.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Context().Return(ctx).AnyTimes()
		gomock.InOrder(
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
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
			}, nil),
			server.EXPECT().Recv().Return(nil, io.EOF),
		)
		server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
			Type: &dag_pb.UploadDagsResponse_Handshake_{
				Handshake: &dag_pb.UploadDagsResponse_Handshake{
					MaximumUnfinalizedDagsCount: 5,
				},
			},
		}))

		require.NoError(t, uploaderServer.UploadDags(server))
	})

	t.Run("InvalidSubsequentRequest", func(t *testing.T) {
		// After handshaking has succeeded, the client may only
		// sent messages of type InitiateDag or
		// ProvideObjectContents.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Context().Return(ctx).AnyTimes()
		gomock.InOrder(
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
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
			}, nil),
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{}, nil),
		)
		server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
			Type: &dag_pb.UploadDagsResponse_Handshake_{
				Handshake: &dag_pb.UploadDagsResponse_Handshake{
					MaximumUnfinalizedDagsCount: 5,
				},
			},
		}))

		testutil.RequireEqualStatus(
			t,
			status.Error(codes.InvalidArgument, "Client sent a message of an unknown type"),
			uploaderServer.UploadDags(server),
		)
	})

	t.Run("InitiateDAGBadRootReferences", func(t *testing.T) {
		// Send an InitiateDag message that does not contain a
		// valid root reference. The server should allocate a
		// reference index for it and send a RequestObject
		// message regardless. The error message should be
		// reported through FinalizeDag.
		for _, e := range []struct {
			rootReference []byte
			statusMessage string
		}{
			{
				rootReference: nil,
				statusMessage: "Invalid root reference: Reference is 0 bytes in size, while SHA256_V1 references are 40 bytes in size",
			},
			{
				rootReference: []byte{
					// SHA-256 hash.
					0x23, 0x1c, 0xf1, 0x15, 0x9b, 0x86, 0x3d, 0x02,
					0x97, 0xae, 0xa6, 0x0a, 0x42, 0xc7, 0xfe, 0x3c,
					0xef, 0x09, 0x25, 0x0e, 0x49, 0x68, 0xf0, 0xaf,
					0x55, 0xe1, 0x65, 0xcb, 0xd0, 0xba, 0xad, 0x7e,
					// Size in bytes.
					0x00, 0x00, 0x10,
					// Height.
					0x0b,
					// Degree.
					0x1a, 0x10,
					// Maximum total parents size in bytes.
					0x03, 0x5f,
				},
				statusMessage: "Height or maximum total parents size of the object exceeds the limit that was established during handshaking",
			},
			{
				rootReference: []byte{
					// SHA-256 hash.
					0xec, 0x48, 0x2e, 0x97, 0x26, 0x58, 0x9e, 0x61,
					0x8c, 0x0a, 0x0a, 0x7d, 0x27, 0x56, 0xdc, 0xc5,
					0x4b, 0x01, 0x57, 0xc1, 0xe2, 0xc4, 0xcc, 0x37,
					0xe3, 0x03, 0x08, 0xc5, 0x89, 0xd8, 0x65, 0x75,
					// Size in bytes.
					0x00, 0x00, 0x10,
					// Height.
					0x0a,
					// Degree.
					0xbe, 0x30,
					// Maximum total parents size in bytes.
					0x06, 0x5e,
				},
				statusMessage: "Height or maximum total parents size of the object exceeds the limit that was established during handshaking",
			},
		} {
			server := NewMockUploader_UploadDagsServer(ctrl)
			server.EXPECT().Context().Return(ctx).AnyTimes()
			wait := make(chan struct{})
			gomock.InOrder(
				server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
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
				}, nil),
				server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_InitiateDag_{
						InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
							RootReference: e.rootReference,
						},
					},
				}, nil),
				server.EXPECT().Recv().
					Do(func() { <-wait }).
					Return(nil, io.EOF),
			)
			gomock.InOrder(
				server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
					Type: &dag_pb.UploadDagsResponse_Handshake_{
						Handshake: &dag_pb.UploadDagsResponse_Handshake{
							MaximumUnfinalizedDagsCount: 5,
						},
					},
				})),
				server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
					Type: &dag_pb.UploadDagsResponse_RequestObject_{
						RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
							LowestReferenceIndex: 0,
							RequestContents:      false,
						},
					},
				})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait) }),
				server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
					Type: &dag_pb.UploadDagsResponse_FinalizeDag_{
						FinalizeDag: &dag_pb.UploadDagsResponse_FinalizeDag{
							RootReferenceIndex: 0,
							Status:             status.New(codes.InvalidArgument, e.statusMessage).Proto(),
						},
					},
				})),
			)

			require.NoError(t, uploaderServer.UploadDags(server))
		}
	})

	t.Run("TooManyInitiateDAGs", func(t *testing.T) {
		// During the handshake process, the server returns the
		// maximum number of unfinalized DAGs a client may have.
		// If the client keeps on sending InitializeDag
		// messages, it could exhaust resources on the server.
		// The server should reject this.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Context().Return(ctx).AnyTimes()
		gomock.InOrder(
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
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
			}, nil),
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_InitiateDag_{
					InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
						RootReference: []byte{
							// SHA-256 hash.
							0x18, 0x5f, 0x8d, 0xb3, 0x22, 0x71, 0xfe, 0x25,
							0xf5, 0x61, 0xa6, 0xfc, 0x93, 0x8b, 0x2e, 0x26,
							0x43, 0x06, 0xec, 0x30, 0x4e, 0xda, 0x51, 0x80,
							0x07, 0xd1, 0x76, 0x48, 0x26, 0x38, 0x19, 0x69,
							// Size in bytes.
							0x05, 0x00, 0x00,
							// Height.
							0x00,
							// Degree.
							0x00, 0x00,
							// Maximum total parents size in bytes.
							0x00, 0x00,
						},
					},
				},
			}, nil).Times(6),
		)
		gomock.InOrder(
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_Handshake_{
					Handshake: &dag_pb.UploadDagsResponse_Handshake{
						MaximumUnfinalizedDagsCount: 5,
					},
				},
			})),
			server.EXPECT().Send(gomock.Any()).MaxTimes(10),
		)
		objectUploader.EXPECT().
			UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969", 5, 0, 0, 0), nil, nil, false).
			Do(func(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases []any, wantContentsIfIncomplete bool) {
				<-ctx.Done()
			}).
			Return(nil, status.Error(codes.Canceled, "Request canceled")).
			MaxTimes(1)

		testutil.RequireEqualStatus(
			t,
			status.Error(codes.InvalidArgument, "Client did not respect the maximum unfinalized DAGs count that was established during handshaking"),
			uploaderServer.UploadDags(server),
		)
	})

	t.Run("SuccessSimpleAlreadyExists", func(t *testing.T) {
		// Attempt to upload a simple DAG of height zero, which
		// already happens to exist. The client does not need to
		// provide the object contents. The server should only
		// write a tag into TagStore.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Context().Return(ctx).AnyTimes()
		wait := make(chan struct{})
		gomock.InOrder(
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
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
			}, nil),
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_InitiateDag_{
					InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
						RootReference: []byte{
							// SHA-256 hash.
							0x4f, 0xee, 0xb0, 0x3e, 0xce, 0x69, 0x22, 0x9a,
							0x2d, 0x98, 0x43, 0xf5, 0xfb, 0xf6, 0xa1, 0xf2,
							0x3c, 0x87, 0x25, 0x9a, 0x1a, 0x59, 0xd7, 0x32,
							0x4d, 0xf6, 0x36, 0x35, 0x3a, 0xd9, 0xa6, 0xb9,
							// Size in bytes.
							0x05, 0x00, 0x00,
							// Height.
							0x00,
							// Degree.
							0x00, 0x00,
							// Maximum total parents size in bytes.
							0x00, 0x00,
						},
						RootTag: &dag_pb.UploadDagsRequest_InitiateDag_Tag{
							Key: &tag_pb.Key{
								SignaturePublicKey: []byte{
									// PKIX header.
									0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65,
									0x70, 0x03, 0x21, 0x00,
									// Ed25519 public key.
									0x05, 0x68, 0xa7, 0xfe, 0x82, 0x5a, 0xda, 0x72,
									0x37, 0x0b, 0x8e, 0xc5, 0x17, 0x33, 0xf4, 0x7b,
									0x08, 0x81, 0x18, 0xb4, 0xd4, 0xd9, 0x3c, 0x69,
									0x24, 0x7e, 0x14, 0x10, 0x69, 0x15, 0x93, 0x4a,
								},
								Hash: []byte{
									0xd0, 0xb7, 0x0d, 0xf9, 0x49, 0x8e, 0x1a, 0xdb,
									0xdc, 0x10, 0xb9, 0xf5, 0xfc, 0xe9, 0xc4, 0x84,
									0xf4, 0xc0, 0x8e, 0x2b, 0x39, 0xb6, 0x9d, 0xc2,
									0x17, 0x19, 0x74, 0x0c, 0xbb, 0x61, 0xff, 0x66,
								},
							},
							Timestamp: &timestamppb.Timestamp{Seconds: 1762241583},
							Signature: []byte{
								0x66, 0x88, 0x42, 0x47, 0x62, 0x4f, 0xb7, 0x91,
								0x5d, 0x56, 0x03, 0x3c, 0xf9, 0x8f, 0x39, 0xcd,
								0x8f, 0xed, 0x24, 0x1e, 0x25, 0x38, 0xeb, 0x5a,
								0xd2, 0x11, 0x30, 0x92, 0xbb, 0x02, 0x38, 0x51,
								0xab, 0x3e, 0xb2, 0x0f, 0x8d, 0x8d, 0xc3, 0xe8,
								0x18, 0x56, 0x68, 0x2e, 0xe3, 0x70, 0x17, 0x93,
								0x45, 0x6d, 0xbc, 0xdf, 0xfa, 0xda, 0xdd, 0xc0,
								0x29, 0x0c, 0xa5, 0x15, 0x08, 0xe8, 0x6d, 0x09,
							},
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait }).
				Return(nil, io.EOF),
		)
		gomock.InOrder(
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_Handshake_{
					Handshake: &dag_pb.UploadDagsResponse_Handshake{
						MaximumUnfinalizedDagsCount: 5,
					},
				},
			})),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 0,
						RequestContents:      false,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_FinalizeDag_{
					FinalizeDag: &dag_pb.UploadDagsResponse_FinalizeDag{
						RootReferenceIndex: 0,
					},
				},
			})),
		)
		gomock.InOrder(
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "4feeb03ece69229a2d9843f5fbf6a1f23c87259a1a59d7324df636353ad9a6b9", 5, 0, 0, 0), nil, nil, false).
				Return(object.UploadObjectComplete[any]{Lease: "Lease"}, nil),
			tagUpdater.EXPECT().UpdateTag(
				gomock.Any(),
				object.Namespace{
					InstanceName:    object.NewInstanceName("hello/world"),
					ReferenceFormat: object.SHA256V1ReferenceFormat,
				},
				tag.Key{
					SignaturePublicKey: [...]byte{
						0x05, 0x68, 0xa7, 0xfe, 0x82, 0x5a, 0xda, 0x72,
						0x37, 0x0b, 0x8e, 0xc5, 0x17, 0x33, 0xf4, 0x7b,
						0x08, 0x81, 0x18, 0xb4, 0xd4, 0xd9, 0x3c, 0x69,
						0x24, 0x7e, 0x14, 0x10, 0x69, 0x15, 0x93, 0x4a,
					},
					Hash: [...]byte{
						0xd0, 0xb7, 0x0d, 0xf9, 0x49, 0x8e, 0x1a, 0xdb,
						0xdc, 0x10, 0xb9, 0xf5, 0xfc, 0xe9, 0xc4, 0x84,
						0xf4, 0xc0, 0x8e, 0x2b, 0x39, 0xb6, 0x9d, 0xc2,
						0x17, 0x19, 0x74, 0x0c, 0xbb, 0x61, 0xff, 0x66,
					},
				},
				tag.SignedValue{
					Value: tag.Value{
						Reference: object.MustNewSHA256V1LocalReference("4feeb03ece69229a2d9843f5fbf6a1f23c87259a1a59d7324df636353ad9a6b9", 5, 0, 0, 0),
						Timestamp: time.Unix(1762241583, 0).UTC(),
					},
					Signature: [...]byte{
						0x66, 0x88, 0x42, 0x47, 0x62, 0x4f, 0xb7, 0x91,
						0x5d, 0x56, 0x03, 0x3c, 0xf9, 0x8f, 0x39, 0xcd,
						0x8f, 0xed, 0x24, 0x1e, 0x25, 0x38, 0xeb, 0x5a,
						0xd2, 0x11, 0x30, 0x92, 0xbb, 0x02, 0x38, 0x51,
						0xab, 0x3e, 0xb2, 0x0f, 0x8d, 0x8d, 0xc3, 0xe8,
						0x18, 0x56, 0x68, 0x2e, 0xe3, 0x70, 0x17, 0x93,
						0x45, 0x6d, 0xbc, 0xdf, 0xfa, 0xda, 0xdd, 0xc0,
						0x29, 0x0c, 0xa5, 0x15, 0x08, 0xe8, 0x6d, 0x09,
					},
				},
				"Lease",
			),
		)

		require.NoError(t, uploaderServer.UploadDags(server))
	})

	t.Run("SuccessSimpleDoesNotExist", func(t *testing.T) {
		// Attempt to upload a simple DAG that does not exist
		// yet. The client should be requested to upload the
		// DAG's contents.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Context().Return(ctx).AnyTimes()
		wait := make(chan struct{})
		gomock.InOrder(
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
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
			}, nil),
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_InitiateDag_{
					InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
						RootReference: []byte{
							// SHA-256 hash.
							0x64, 0xec, 0x88, 0xca, 0x00, 0xb2, 0x68, 0xe5,
							0xba, 0x1a, 0x35, 0x67, 0x8a, 0x1b, 0x53, 0x16,
							0xd2, 0x12, 0xf4, 0xf3, 0x66, 0xb2, 0x47, 0x72,
							0x32, 0x53, 0x4a, 0x8a, 0xec, 0xa3, 0x7f, 0x3c,
							// Size in bytes.
							0x0b, 0x00, 0x00,
							// Height.
							0x00,
							// Degree.
							0x00, 0x00,
							// Maximum total parents size in bytes.
							0x00, 0x00,
						},
						RootTag: &dag_pb.UploadDagsRequest_InitiateDag_Tag{
							Key: &tag_pb.Key{
								SignaturePublicKey: []byte{
									// PKIX header.
									0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65,
									0x70, 0x03, 0x21, 0x00,
									// Ed25519 public key.
									0x04, 0x2a, 0x5f, 0xab, 0x95, 0xc4, 0xd2, 0xff,
									0x5d, 0x8e, 0x84, 0x38, 0x25, 0xa7, 0xc9, 0x94,
									0x46, 0x8a, 0xd0, 0xf1, 0x78, 0x98, 0x0f, 0x9e,
									0x87, 0x74, 0x34, 0xef, 0x19, 0x1e, 0xa6, 0x3a,
								},
								Hash: []byte{
									0x54, 0xb7, 0x53, 0x2a, 0x71, 0xca, 0xac, 0xf7,
									0xd9, 0x96, 0xfb, 0x3b, 0x80, 0x6b, 0xb4, 0x1d,
									0x59, 0x57, 0xa4, 0xf8, 0x5b, 0x9f, 0x5a, 0xc0,
									0xa0, 0xeb, 0x18, 0xd1, 0x9c, 0x63, 0xbc, 0xab,
								},
							},
							Timestamp: &timestamppb.Timestamp{Seconds: 1762241722},
							Signature: []byte{
								0x9b, 0x67, 0x94, 0x30, 0xa9, 0x51, 0xec, 0x6a,
								0x7a, 0x71, 0x6d, 0x94, 0x4d, 0x9d, 0x3d, 0xee,
								0xda, 0x0b, 0xa0, 0x5e, 0x7c, 0x0e, 0x4c, 0xf9,
								0x20, 0xdd, 0x34, 0x07, 0xff, 0x30, 0x84, 0x16,
								0x3d, 0xa3, 0x9d, 0x52, 0x94, 0x71, 0xa0, 0x28,
								0xfd, 0xff, 0xb1, 0xcc, 0x48, 0xce, 0x3b, 0x54,
								0x73, 0x25, 0x2c, 0x0b, 0xec, 0xe9, 0x4e, 0x37,
								0x90, 0x1d, 0x1c, 0x30, 0x87, 0xa1, 0x8a, 0x06,
							},
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 0,
							ObjectContents:       []byte("Hello world"),
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Return(nil, io.EOF),
		)
		gomock.InOrder(
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_Handshake_{
					Handshake: &dag_pb.UploadDagsResponse_Handshake{
						MaximumUnfinalizedDagsCount: 5,
					},
				},
			})),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 0,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_FinalizeDag_{
					FinalizeDag: &dag_pb.UploadDagsResponse_FinalizeDag{
						RootReferenceIndex: 0,
					},
				},
			})),
		)
		gomock.InOrder(
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "64ec88ca00b268e5ba1a35678a1b5316d212f4f366b2477232534a8aeca37f3c", 11, 0, 0, 0), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "64ec88ca00b268e5ba1a35678a1b5316d212f4f366b2477232534a8aeca37f3c", 11, 0, 0, 0), gomock.Any(), gomock.Len(0), false).
				Do(func(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases []any, wantContentsIfIncomplete bool) {
					require.Equal(t, object.MustNewSHA256V1LocalReference("64ec88ca00b268e5ba1a35678a1b5316d212f4f366b2477232534a8aeca37f3c", 11, 0, 0, 0), contents.GetLocalReference())
					require.Equal(t, []byte("Hello world"), contents.GetPayload())
				}).
				Return(object.UploadObjectComplete[any]{Lease: "Lease"}, nil),
			tagUpdater.EXPECT().UpdateTag(
				gomock.Any(),
				object.Namespace{
					InstanceName:    object.NewInstanceName("hello/world"),
					ReferenceFormat: object.SHA256V1ReferenceFormat,
				},
				tag.Key{
					SignaturePublicKey: [...]byte{
						0x04, 0x2a, 0x5f, 0xab, 0x95, 0xc4, 0xd2, 0xff,
						0x5d, 0x8e, 0x84, 0x38, 0x25, 0xa7, 0xc9, 0x94,
						0x46, 0x8a, 0xd0, 0xf1, 0x78, 0x98, 0x0f, 0x9e,
						0x87, 0x74, 0x34, 0xef, 0x19, 0x1e, 0xa6, 0x3a,
					},
					Hash: [...]byte{
						0x54, 0xb7, 0x53, 0x2a, 0x71, 0xca, 0xac, 0xf7,
						0xd9, 0x96, 0xfb, 0x3b, 0x80, 0x6b, 0xb4, 0x1d,
						0x59, 0x57, 0xa4, 0xf8, 0x5b, 0x9f, 0x5a, 0xc0,
						0xa0, 0xeb, 0x18, 0xd1, 0x9c, 0x63, 0xbc, 0xab,
					},
				},
				tag.SignedValue{
					Value: tag.Value{
						Reference: object.MustNewSHA256V1LocalReference("64ec88ca00b268e5ba1a35678a1b5316d212f4f366b2477232534a8aeca37f3c", 11, 0, 0, 0),
						Timestamp: time.Unix(1762241722, 0).UTC(),
					},
					Signature: [...]byte{
						0x9b, 0x67, 0x94, 0x30, 0xa9, 0x51, 0xec, 0x6a,
						0x7a, 0x71, 0x6d, 0x94, 0x4d, 0x9d, 0x3d, 0xee,
						0xda, 0x0b, 0xa0, 0x5e, 0x7c, 0x0e, 0x4c, 0xf9,
						0x20, 0xdd, 0x34, 0x07, 0xff, 0x30, 0x84, 0x16,
						0x3d, 0xa3, 0x9d, 0x52, 0x94, 0x71, 0xa0, 0x28,
						0xfd, 0xff, 0xb1, 0xcc, 0x48, 0xce, 0x3b, 0x54,
						0x73, 0x25, 0x2c, 0x0b, 0xec, 0xe9, 0x4e, 0x37,
						0x90, 0x1d, 0x1c, 0x30, 0x87, 0xa1, 0x8a, 0x06,
					},
				},
				"Lease",
			),
		)

		require.NoError(t, uploaderServer.UploadDags(server))
	})

	t.Run("ClientCancelation", func(t *testing.T) {
		// If the client wants to cancel the upload of a given
		// DAG without terminating the request entirely, it can
		// leave ObjectContents unset. This should cause
		// FinalizeDag to contain a CANCELLED error.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Context().Return(ctx).AnyTimes()
		wait := make(chan struct{})
		gomock.InOrder(
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
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
			}, nil),
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_InitiateDag_{
					InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
						RootReference: []byte{
							// SHA-256 hash.
							0x20, 0x7c, 0xef, 0xdb, 0xee, 0x02, 0xec, 0xe5,
							0xb6, 0x44, 0xdd, 0xc2, 0x78, 0x36, 0x28, 0x22,
							0xe6, 0xf5, 0x17, 0xdf, 0x8b, 0xbe, 0x3d, 0xe0,
							0xbc, 0x11, 0xfe, 0xe7, 0x72, 0x58, 0x1b, 0xea,
							// Size in bytes.
							0xf1, 0x00, 0x00,
							// Height.
							0x00,
							// Degree.
							0x00, 0x00,
							// Maximum total parents size in bytes.
							0x00, 0x00,
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 0,
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Return(nil, io.EOF),
		)
		gomock.InOrder(
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_Handshake_{
					Handshake: &dag_pb.UploadDagsResponse_Handshake{
						MaximumUnfinalizedDagsCount: 5,
					},
				},
			})),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 0,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_FinalizeDag_{
					FinalizeDag: &dag_pb.UploadDagsResponse_FinalizeDag{
						RootReferenceIndex: 0,
						Status:             status.New(codes.Canceled, "Client canceled upload of object with reference SHA256=207cefdbee02ece5b644ddc278362822e6f517df8bbe3de0bc11fee772581bea:S=241:H=0:D=0:M=0").Proto(),
					},
				},
			})),
		)
		objectUploader.EXPECT().
			UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "207cefdbee02ece5b644ddc278362822e6f517df8bbe3de0bc11fee772581bea", 241, 0, 0, 0), nil, nil, false).
			Return(object.UploadObjectMissing[any]{}, nil)

		require.NoError(t, uploaderServer.UploadDags(server))
	})

	t.Run("ChecksumMismatch", func(t *testing.T) {
		// The server should validate the checksum of any data
		// that is uploaded. Any mismatches should be a hard
		// error that immediately terminate the transmission, as
		// there is no graceful way we can recover from this.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Context().Return(ctx).AnyTimes()
		wait := make(chan struct{})
		gomock.InOrder(
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
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
			}, nil),
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_InitiateDag_{
					InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
						RootReference: []byte{
							// SHA-256 hash.
							0x00, 0xef, 0xce, 0x81, 0x7c, 0xc6, 0x81, 0xdf,
							0xe9, 0xca, 0xa0, 0x81, 0xc7, 0xbf, 0x41, 0x2e,
							0xfa, 0x02, 0xb9, 0x18, 0xb6, 0xcb, 0x8c, 0x8c,
							0xab, 0x53, 0x73, 0xfc, 0x96, 0x88, 0x0a, 0x47,
							// Size in bytes.
							0x2e, 0x00, 0x00,
							// Height.
							0x00,
							// Degree.
							0x00, 0x00,
							// Maximum total parents size in bytes.
							0x00, 0x00,
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 0,
							ObjectContents:       []byte("This data does not match the provided checksum"),
						},
					},
				}, nil),
		)
		gomock.InOrder(
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_Handshake_{
					Handshake: &dag_pb.UploadDagsResponse_Handshake{
						MaximumUnfinalizedDagsCount: 5,
					},
				},
			})),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 0,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait) }),
		)
		objectUploader.EXPECT().
			UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "00efce817cc681dfe9caa081c7bf412efa02b918b6cb8c8cab5373fc96880a47", 46, 0, 0, 0), nil, nil, false).
			Return(object.UploadObjectMissing[any]{}, nil)

		testutil.RequireEqualStatus(
			t,
			status.Error(codes.InvalidArgument, "Invalid contents for object with reference SHA256=00efce817cc681dfe9caa081c7bf412efa02b918b6cb8c8cab5373fc96880a47:S=46:H=0:D=0:M=0: Data has SHA-256 hash 241491f78c0a90c8ed711402bef696824eb83edc214586d449b289fccecce32d, while 00efce817cc681dfe9caa081c7bf412efa02b918b6cb8c8cab5373fc96880a47 was expected"),
			uploaderServer.UploadDags(server),
		)
	})

	t.Run("LimitedMaximimUnfinalizedParents", func(t *testing.T) {
		// Upload a DAG having the following shape, where the
		// numbers correspond with the reference indices:
		//
		//                         0
		//                       / | \
		//                       1 2 3
		//                       | | |
		//                       4 5 6
		//
		// If MaximumUnfinalizedParents{Count,SizeBytes} is set
		// sufficiently small, UploadDags() has no choice but to
		// ingest this DAG depth first. This means that the
		// objects should be requested in the following order:
		//
		//                   0 1 4 2 5 3 6
		//
		// And written to storage in the following order:
		//
		//                   4 1 5 2 6 3 0
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Context().Return(ctx).AnyTimes()
		wait0 := make(chan struct{})
		wait1 := make(chan struct{})
		wait2 := make(chan struct{})
		wait3 := make(chan struct{})
		wait4 := make(chan struct{})
		wait5 := make(chan struct{})
		wait6 := make(chan struct{})
		gomock.InOrder(
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_Handshake_{
					Handshake: &dag_pb.UploadDagsRequest_Handshake{
						Namespace: &object_pb.Namespace{
							InstanceName:    "hello/world",
							ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
						},
						MaximumUnfinalizedParentsLimit: &object_pb.Limit{
							Count:     2,
							SizeBytes: 160,
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_InitiateDag_{
					InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
						RootReference: []byte{
							// SHA-256 hash.
							0xe8, 0x2d, 0x79, 0xbe, 0xda, 0xc8, 0x41, 0xc3,
							0x65, 0x92, 0x22, 0xe8, 0xa9, 0xe3, 0x2d, 0x1e,
							0xe3, 0x0d, 0xb6, 0x4c, 0x37, 0xc7, 0xca, 0x7e,
							0xa2, 0x44, 0x53, 0x1d, 0x33, 0x40, 0x76, 0xa5,
							// Size in bytes.
							0x78, 0x00, 0x00,
							// Height.
							0x02,
							// Degree.
							0x03, 0x00,
							// Maximum total parents size in bytes.
							0x28, 0x00,
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait0 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 0,
							ObjectContents: []byte{
								// SHA-256 hash.
								0x0a, 0x2d, 0xf7, 0xd6, 0x23, 0xcd, 0xdd, 0xd4,
								0x5c, 0x7d, 0x8a, 0x7f, 0xbf, 0x80, 0x4e, 0x8f,
								0x9c, 0x80, 0xf7, 0xb1, 0x02, 0x00, 0xe3, 0x62,
								0x34, 0x06, 0xd0, 0x8d, 0x3e, 0xf8, 0x03, 0x79,
								// Size in bytes.
								0x28, 0x00, 0x00,
								// Height.
								0x01,
								// Degree.
								0x01, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,

								// SHA-256 hash.
								0x43, 0xc4, 0x78, 0xf5, 0xae, 0x1c, 0x10, 0xfc,
								0x88, 0x5c, 0x62, 0x45, 0x88, 0x47, 0xe1, 0xab,
								0xc4, 0xf9, 0x9b, 0x5f, 0xd6, 0x6e, 0x92, 0x4f,
								0x42, 0xc1, 0x4b, 0xbe, 0xae, 0xe2, 0xcb, 0x08,
								// Size in bytes.
								0x28, 0x00, 0x00,
								// Height.
								0x01,
								// Degree.
								0x01, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,

								// SHA-256 hash.
								0xcc, 0x38, 0xaa, 0xed, 0x6e, 0xf7, 0x40, 0x3d,
								0xf8, 0xe1, 0xc7, 0xd7, 0x3b, 0xc8, 0xc6, 0x3c,
								0x08, 0x28, 0x49, 0x1a, 0x1b, 0xf3, 0x52, 0x6f,
								0xcf, 0xbc, 0xe6, 0x85, 0xcd, 0xc3, 0xbc, 0x45,
								// Size in bytes.
								0x28, 0x00, 0x00,
								// Height.
								0x01,
								// Degree.
								0x01, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,
							},
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait1 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 1,
							ObjectContents: []byte{
								// SHA-256 hash.
								0xbc, 0x54, 0x1a, 0x4c, 0x77, 0xcb, 0x12, 0x7b,
								0x44, 0x3a, 0xb4, 0xa1, 0x29, 0x5c, 0x96, 0x96,
								0xc1, 0xea, 0xbf, 0x12, 0xb1, 0xc0, 0x3d, 0xa5,
								0xaf, 0x79, 0x0b, 0x7c, 0xdb, 0x3b, 0xf6, 0xb3,
								// Size in bytes.
								0x03, 0x00, 0x00,
								// Height.
								0x00,
								// Degree.
								0x00, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,
							},
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait4 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 4,
							ObjectContents:       []byte("Baz"),
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait2 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 2,
							ObjectContents: []byte{
								// SHA-256 hash.
								0x1c, 0xbe, 0xc7, 0x37, 0xf8, 0x63, 0xe4, 0x92,
								0x2c, 0xee, 0x63, 0xcc, 0x2e, 0xbb, 0xfa, 0xaf,
								0xcd, 0x1c, 0xff, 0x8b, 0x79, 0x0d, 0x8c, 0xfd,
								0x2e, 0x6a, 0x5d, 0x55, 0x0b, 0x64, 0x8a, 0xfa,
								// Size in bytes.
								0x03, 0x00, 0x00,
								// Height.
								0x00,
								// Degree.
								0x00, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,
							},
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait5 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 5,
							ObjectContents:       []byte("Foo"),
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait3 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 3,
							ObjectContents: []byte{
								// SHA-256 hash.
								0x95, 0xd6, 0x4c, 0xac, 0xce, 0x0f, 0x0e, 0x5b,
								0x0d, 0x1b, 0x84, 0x38, 0x62, 0xf0, 0xac, 0xcf,
								0xad, 0xb7, 0x87, 0xa4, 0xca, 0xbb, 0x8a, 0x88,
								0xf7, 0xf1, 0x69, 0x4e, 0xa2, 0x32, 0xa5, 0xfc,
								// Size in bytes.
								0x03, 0x00, 0x00,
								// Height.
								0x00,
								// Degree.
								0x00, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,
							},
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait6 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 6,
							ObjectContents:       []byte("Bar"),
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Return(nil, io.EOF),
		)
		gomock.InOrder(
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_Handshake_{
					Handshake: &dag_pb.UploadDagsResponse_Handshake{
						MaximumUnfinalizedDagsCount: 5,
					},
				},
			})),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 0,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait0) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 1,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait1) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 4,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait4) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 2,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait2) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 5,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait5) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 3,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait3) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 6,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait6) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_FinalizeDag_{
					FinalizeDag: &dag_pb.UploadDagsResponse_FinalizeDag{
						RootReferenceIndex: 0,
					},
				},
			})),
		)
		gomock.InOrder(
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "e82d79bedac841c3659222e8a9e32d1ee30db64c37c7ca7ea244531d334076a5", 120, 2, 3, 40), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "0a2df7d623cdddd45c7d8a7fbf804e8f9c80f7b10200e3623406d08d3ef80379", 40, 1, 1, 0), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "bc541a4c77cb127b443ab4a1295c9696c1eabf12b1c03da5af790b7cdb3bf6b3", 3, 0, 0, 0), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "bc541a4c77cb127b443ab4a1295c9696c1eabf12b1c03da5af790b7cdb3bf6b3", 3, 0, 0, 0), gomock.Any(), gomock.Len(0), false).
				Do(func(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases []any, wantContentsIfIncomplete bool) {
					require.Equal(t, object.MustNewSHA256V1LocalReference("bc541a4c77cb127b443ab4a1295c9696c1eabf12b1c03da5af790b7cdb3bf6b3", 3, 0, 0, 0), contents.GetLocalReference())
					require.Equal(t, []byte("Baz"), contents.GetPayload())
				}).
				Return(object.UploadObjectComplete[any]{Lease: "Lease 4"}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "0a2df7d623cdddd45c7d8a7fbf804e8f9c80f7b10200e3623406d08d3ef80379", 40, 1, 1, 0), gomock.Any(), []any{"Lease 4"}, false).
				Do(func(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases []any, wantContentsIfIncomplete bool) {
					require.Equal(t, object.MustNewSHA256V1LocalReference("0a2df7d623cdddd45c7d8a7fbf804e8f9c80f7b10200e3623406d08d3ef80379", 40, 1, 1, 0), contents.GetLocalReference())
					require.Equal(t, object.MustNewSHA256V1LocalReference("bc541a4c77cb127b443ab4a1295c9696c1eabf12b1c03da5af790b7cdb3bf6b3", 3, 0, 0, 0), contents.GetOutgoingReference(0))
					require.Empty(t, contents.GetPayload())
				}).
				Return(object.UploadObjectComplete[any]{Lease: "Lease 1"}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "43c478f5ae1c10fc885c62458847e1abc4f99b5fd66e924f42c14bbeaee2cb08", 40, 1, 1, 0), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "1cbec737f863e4922cee63cc2ebbfaafcd1cff8b790d8cfd2e6a5d550b648afa", 3, 0, 0, 0), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "1cbec737f863e4922cee63cc2ebbfaafcd1cff8b790d8cfd2e6a5d550b648afa", 3, 0, 0, 0), gomock.Any(), gomock.Len(0), false).
				Do(func(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases []any, wantContentsIfIncomplete bool) {
					require.Equal(t, object.MustNewSHA256V1LocalReference("1cbec737f863e4922cee63cc2ebbfaafcd1cff8b790d8cfd2e6a5d550b648afa", 3, 0, 0, 0), contents.GetLocalReference())
					require.Equal(t, []byte("Foo"), contents.GetPayload())
				}).
				Return(object.UploadObjectComplete[any]{Lease: "Lease 5"}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "43c478f5ae1c10fc885c62458847e1abc4f99b5fd66e924f42c14bbeaee2cb08", 40, 1, 1, 0), gomock.Any(), []any{"Lease 5"}, false).
				Do(func(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases []any, wantContentsIfIncomplete bool) {
					require.Equal(t, object.MustNewSHA256V1LocalReference("43c478f5ae1c10fc885c62458847e1abc4f99b5fd66e924f42c14bbeaee2cb08", 40, 1, 1, 0), contents.GetLocalReference())
					require.Equal(t, object.MustNewSHA256V1LocalReference("1cbec737f863e4922cee63cc2ebbfaafcd1cff8b790d8cfd2e6a5d550b648afa", 3, 0, 0, 0), contents.GetOutgoingReference(0))
					require.Empty(t, contents.GetPayload())
				}).
				Return(object.UploadObjectComplete[any]{Lease: "Lease 2"}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "cc38aaed6ef7403df8e1c7d73bc8c63c0828491a1bf3526fcfbce685cdc3bc45", 40, 1, 1, 0), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "95d64cacce0f0e5b0d1b843862f0accfadb787a4cabb8a88f7f1694ea232a5fc", 3, 0, 0, 0), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "95d64cacce0f0e5b0d1b843862f0accfadb787a4cabb8a88f7f1694ea232a5fc", 3, 0, 0, 0), gomock.Any(), gomock.Len(0), false).
				Do(func(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases []any, wantContentsIfIncomplete bool) {
					require.Equal(t, object.MustNewSHA256V1LocalReference("95d64cacce0f0e5b0d1b843862f0accfadb787a4cabb8a88f7f1694ea232a5fc", 3, 0, 0, 0), contents.GetLocalReference())
					require.Equal(t, []byte("Bar"), contents.GetPayload())
				}).
				Return(object.UploadObjectComplete[any]{Lease: "Lease 6"}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "cc38aaed6ef7403df8e1c7d73bc8c63c0828491a1bf3526fcfbce685cdc3bc45", 40, 1, 1, 0), gomock.Any(), []any{"Lease 6"}, false).
				Do(func(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases []any, wantContentsIfIncomplete bool) {
					require.Equal(t, object.MustNewSHA256V1LocalReference("cc38aaed6ef7403df8e1c7d73bc8c63c0828491a1bf3526fcfbce685cdc3bc45", 40, 1, 1, 0), contents.GetLocalReference())
					require.Equal(t, object.MustNewSHA256V1LocalReference("95d64cacce0f0e5b0d1b843862f0accfadb787a4cabb8a88f7f1694ea232a5fc", 3, 0, 0, 0), contents.GetOutgoingReference(0))
					require.Empty(t, contents.GetPayload())
				}).
				Return(object.UploadObjectComplete[any]{Lease: "Lease 3"}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "e82d79bedac841c3659222e8a9e32d1ee30db64c37c7ca7ea244531d334076a5", 120, 2, 3, 40), gomock.Any(), []any{"Lease 1", "Lease 2", "Lease 3"}, false).
				Do(func(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases []any, wantContentsIfIncomplete bool) {
					require.Equal(t, object.MustNewSHA256V1LocalReference("e82d79bedac841c3659222e8a9e32d1ee30db64c37c7ca7ea244531d334076a5", 120, 2, 3, 40), contents.GetLocalReference())
					require.Equal(t, object.MustNewSHA256V1LocalReference("0a2df7d623cdddd45c7d8a7fbf804e8f9c80f7b10200e3623406d08d3ef80379", 40, 1, 1, 0), contents.GetOutgoingReference(0))
					require.Equal(t, object.MustNewSHA256V1LocalReference("43c478f5ae1c10fc885c62458847e1abc4f99b5fd66e924f42c14bbeaee2cb08", 40, 1, 1, 0), contents.GetOutgoingReference(1))
					require.Equal(t, object.MustNewSHA256V1LocalReference("cc38aaed6ef7403df8e1c7d73bc8c63c0828491a1bf3526fcfbce685cdc3bc45", 40, 1, 1, 0), contents.GetOutgoingReference(2))
					require.Empty(t, contents.GetPayload())
				}).
				Return(object.UploadObjectComplete[any]{Lease: "Lease 0"}, nil),
		)

		require.NoError(t, uploaderServer.UploadDags(server))
	})

	t.Run("LimitedMaximimUnfinalizedParentsAllChildrenExist", func(t *testing.T) {
		// The same test as the one above, except that the
		// object store reports that objects 1, 2 and 3 are all
		// already present.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Context().Return(ctx).AnyTimes()
		wait0 := make(chan struct{})
		wait3 := make(chan struct{})
		gomock.InOrder(
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_Handshake_{
					Handshake: &dag_pb.UploadDagsRequest_Handshake{
						Namespace: &object_pb.Namespace{
							InstanceName:    "hello/world",
							ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
						},
						MaximumUnfinalizedParentsLimit: &object_pb.Limit{
							Count:     2,
							SizeBytes: 160,
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_InitiateDag_{
					InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
						RootReference: []byte{
							// SHA-256 hash.
							0xe8, 0x2d, 0x79, 0xbe, 0xda, 0xc8, 0x41, 0xc3,
							0x65, 0x92, 0x22, 0xe8, 0xa9, 0xe3, 0x2d, 0x1e,
							0xe3, 0x0d, 0xb6, 0x4c, 0x37, 0xc7, 0xca, 0x7e,
							0xa2, 0x44, 0x53, 0x1d, 0x33, 0x40, 0x76, 0xa5,
							// Size in bytes.
							0x78, 0x00, 0x00,
							// Height.
							0x02,
							// Degree.
							0x03, 0x00,
							// Maximum total parents size in bytes.
							0x28, 0x00,
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait0 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 0,
							ObjectContents: []byte{
								// SHA-256 hash.
								0x0a, 0x2d, 0xf7, 0xd6, 0x23, 0xcd, 0xdd, 0xd4,
								0x5c, 0x7d, 0x8a, 0x7f, 0xbf, 0x80, 0x4e, 0x8f,
								0x9c, 0x80, 0xf7, 0xb1, 0x02, 0x00, 0xe3, 0x62,
								0x34, 0x06, 0xd0, 0x8d, 0x3e, 0xf8, 0x03, 0x79,
								// Size in bytes.
								0x28, 0x00, 0x00,
								// Height.
								0x01,
								// Degree.
								0x01, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,

								// SHA-256 hash.
								0x43, 0xc4, 0x78, 0xf5, 0xae, 0x1c, 0x10, 0xfc,
								0x88, 0x5c, 0x62, 0x45, 0x88, 0x47, 0xe1, 0xab,
								0xc4, 0xf9, 0x9b, 0x5f, 0xd6, 0x6e, 0x92, 0x4f,
								0x42, 0xc1, 0x4b, 0xbe, 0xae, 0xe2, 0xcb, 0x08,
								// Size in bytes.
								0x28, 0x00, 0x00,
								// Height.
								0x01,
								// Degree.
								0x01, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,

								// SHA-256 hash.
								0xcc, 0x38, 0xaa, 0xed, 0x6e, 0xf7, 0x40, 0x3d,
								0xf8, 0xe1, 0xc7, 0xd7, 0x3b, 0xc8, 0xc6, 0x3c,
								0x08, 0x28, 0x49, 0x1a, 0x1b, 0xf3, 0x52, 0x6f,
								0xcf, 0xbc, 0xe6, 0x85, 0xcd, 0xc3, 0xbc, 0x45,
								// Size in bytes.
								0x28, 0x00, 0x00,
								// Height.
								0x01,
								// Degree.
								0x01, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,
							},
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait3 }).
				Return(nil, io.EOF),
		)
		gomock.InOrder(
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_Handshake_{
					Handshake: &dag_pb.UploadDagsResponse_Handshake{
						MaximumUnfinalizedDagsCount: 5,
					},
				},
			})),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 0,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait0) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 1,
						RequestContents:      false,
					},
				},
			})),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 2,
						RequestContents:      false,
					},
				},
			})),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 3,
						RequestContents:      false,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait3) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_FinalizeDag_{
					FinalizeDag: &dag_pb.UploadDagsResponse_FinalizeDag{
						RootReferenceIndex: 0,
					},
				},
			})),
		)
		gomock.InOrder(
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "e82d79bedac841c3659222e8a9e32d1ee30db64c37c7ca7ea244531d334076a5", 120, 2, 3, 40), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "0a2df7d623cdddd45c7d8a7fbf804e8f9c80f7b10200e3623406d08d3ef80379", 40, 1, 1, 0), nil, nil, false).
				Return(object.UploadObjectComplete[any]{
					Lease: "Lease 1",
				}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "43c478f5ae1c10fc885c62458847e1abc4f99b5fd66e924f42c14bbeaee2cb08", 40, 1, 1, 0), nil, nil, false).
				Return(object.UploadObjectComplete[any]{
					Lease: "Lease 2",
				}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "cc38aaed6ef7403df8e1c7d73bc8c63c0828491a1bf3526fcfbce685cdc3bc45", 40, 1, 1, 0), nil, nil, false).
				Return(object.UploadObjectComplete[any]{
					Lease: "Lease 3",
				}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "e82d79bedac841c3659222e8a9e32d1ee30db64c37c7ca7ea244531d334076a5", 120, 2, 3, 40), gomock.Any(), []any{"Lease 1", "Lease 2", "Lease 3"}, false).
				Do(func(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases []any, wantContentsIfIncomplete bool) {
					require.Equal(t, object.MustNewSHA256V1LocalReference("e82d79bedac841c3659222e8a9e32d1ee30db64c37c7ca7ea244531d334076a5", 120, 2, 3, 40), contents.GetLocalReference())
					require.Equal(t, object.MustNewSHA256V1LocalReference("0a2df7d623cdddd45c7d8a7fbf804e8f9c80f7b10200e3623406d08d3ef80379", 40, 1, 1, 0), contents.GetOutgoingReference(0))
					require.Equal(t, object.MustNewSHA256V1LocalReference("43c478f5ae1c10fc885c62458847e1abc4f99b5fd66e924f42c14bbeaee2cb08", 40, 1, 1, 0), contents.GetOutgoingReference(1))
					require.Equal(t, object.MustNewSHA256V1LocalReference("cc38aaed6ef7403df8e1c7d73bc8c63c0828491a1bf3526fcfbce685cdc3bc45", 40, 1, 1, 0), contents.GetOutgoingReference(2))
					require.Empty(t, contents.GetPayload())
				}).
				Return(object.UploadObjectComplete[any]{Lease: "Lease 0"}, nil),
		)

		require.NoError(t, uploaderServer.UploadDags(server))
	})

	t.Run("DeepClientCancelation", func(t *testing.T) {
		// If cancelations occur deep inside of a DAG, the error
		// should still be propagated up to FinalizeDag. Parent
		// objects should not be written into object.Store.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Context().Return(ctx).AnyTimes()
		wait0 := make(chan struct{})
		wait1 := make(chan struct{})
		wait2 := make(chan struct{})
		wait3 := make(chan struct{})
		wait4 := make(chan struct{})
		gomock.InOrder(
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_Handshake_{
					Handshake: &dag_pb.UploadDagsRequest_Handshake{
						Namespace: &object_pb.Namespace{
							InstanceName:    "hello/world",
							ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
						},
						MaximumUnfinalizedParentsLimit: &object_pb.Limit{
							Count:     2,
							SizeBytes: 160,
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_InitiateDag_{
					InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
						RootReference: []byte{
							// SHA-256 hash.
							0xe0, 0xe4, 0x74, 0x85, 0xb1, 0xa2, 0x64, 0x8a,
							0xfb, 0xc1, 0x3e, 0x80, 0xa2, 0x78, 0x47, 0xe8,
							0xaf, 0x72, 0xc8, 0x18, 0x4f, 0x19, 0x6f, 0x87,
							0x6d, 0xd2, 0x36, 0xe3, 0xc9, 0xe1, 0x95, 0xdc,
							// Size in bytes.
							0x78, 0x00, 0x00,
							// Height.
							0x02,
							// Degree.
							0x03, 0x00,
							// Maximum total parents size in bytes.
							0x28, 0x00,
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait0 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 0,
							ObjectContents: []byte{
								// SHA-256 hash.
								0x8e, 0x97, 0x60, 0xca, 0x1e, 0x21, 0xd7, 0x2d,
								0xb3, 0x57, 0xcf, 0xc8, 0xc7, 0x49, 0x6b, 0xd2,
								0xcb, 0x91, 0x4e, 0xb4, 0x00, 0x20, 0xf9, 0x28,
								0xd2, 0x7e, 0xcc, 0xd6, 0xbd, 0xc8, 0xfe, 0x9c,
								// Size in bytes.
								0x28, 0x00, 0x00,
								// Height.
								0x01,
								// Degree.
								0x01, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,

								// SHA-256 hash.
								0x9b, 0xab, 0x46, 0x92, 0xa0, 0xfd, 0x9e, 0x9f,
								0xe8, 0x9e, 0x6e, 0x2e, 0xea, 0xe5, 0xa3, 0xdd,
								0x2d, 0x8d, 0xcf, 0xc3, 0x18, 0x0d, 0xae, 0x50,
								0xc0, 0x08, 0x6b, 0xde, 0x9d, 0xde, 0x0e, 0xe3,
								// Size in bytes.
								0x28, 0x00, 0x00,
								// Height.
								0x01,
								// Degree.
								0x01, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,

								// SHA-256 hash.
								0xa1, 0x19, 0xb9, 0x81, 0x1b, 0x67, 0x6d, 0x8b,
								0x70, 0xce, 0xe0, 0x97, 0xcd, 0x03, 0xc9, 0xdf,
								0xa3, 0x55, 0x4f, 0x7a, 0xfa, 0x8e, 0x0f, 0xe2,
								0x85, 0x06, 0x87, 0x84, 0x28, 0xb6, 0xd4, 0x80,
								// Size in bytes.
								0x28, 0x00, 0x00,
								// Height.
								0x01,
								// Degree.
								0x01, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,
							},
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait1 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 1,
							ObjectContents: []byte{
								// SHA-256 hash.
								0xed, 0x32, 0x2e, 0xda, 0xa7, 0x90, 0xee, 0xed,
								0xae, 0x11, 0x61, 0x9e, 0xce, 0xaf, 0x20, 0x98,
								0x1f, 0x74, 0xe5, 0xf2, 0xa7, 0x07, 0x1f, 0x53,
								0x65, 0x59, 0x70, 0x89, 0xd3, 0x49, 0xbe, 0x14,
								// Size in bytes.
								0x20, 0x17, 0x00,
								// Height.
								0x00,
								// Degree.
								0x00, 0x00,
								// Maximum total parents size in bytes.
								0x00, 0x00,
							},
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait4 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 4,
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait2 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 2,
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait3 }).
				Return(&dag_pb.UploadDagsRequest{
					Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
						ProvideObjectContents: &dag_pb.UploadDagsRequest_ProvideObjectContents{
							LowestReferenceIndex: 3,
						},
					},
				}, nil),
			server.EXPECT().Recv().
				Return(nil, io.EOF),
		)
		gomock.InOrder(
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_Handshake_{
					Handshake: &dag_pb.UploadDagsResponse_Handshake{
						MaximumUnfinalizedDagsCount: 5,
					},
				},
			})),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 0,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait0) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 1,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait1) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 4,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait4) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 2,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait2) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 3,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait3) }),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_FinalizeDag_{
					FinalizeDag: &dag_pb.UploadDagsResponse_FinalizeDag{
						RootReferenceIndex: 0,
						Status:             status.New(codes.Canceled, "Client canceled upload of object with reference SHA256=ed322edaa790eeedae11619eceaf20981f74e5f2a7071f5365597089d349be14:S=5920:H=0:D=0:M=0").Proto(),
					},
				},
			})),
		)
		gomock.InOrder(
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "e0e47485b1a2648afbc13e80a27847e8af72c8184f196f876dd236e3c9e195dc", 120, 2, 3, 40), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "8e9760ca1e21d72db357cfc8c7496bd2cb914eb40020f928d27eccd6bdc8fe9c", 40, 1, 1, 0), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "ed322edaa790eeedae11619eceaf20981f74e5f2a7071f5365597089d349be14", 5920, 0, 0, 0), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "9bab4692a0fd9e9fe89e6e2eeae5a3dd2d8dcfc3180dae50c0086bde9dde0ee3", 40, 1, 1, 0), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "a119b9811b676d8b70cee097cd03c9dfa3554f7afa8e0fe28506878428b6d480", 40, 1, 1, 0), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
		)

		require.NoError(t, uploaderServer.UploadDags(server))
	})

	t.Run("PrematureCloseBeforeRequestObject", func(t *testing.T) {
		// Even though it's valid for clients to close the
		// stream for sending before receiving the final
		// FinalizeDag messages, they are not permitted to close
		// it if they still need to receive one or more
		// RequestObject messages. Those might still necessitate
		// the client to send the contents of an object.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Context().Return(ctx).AnyTimes()
		wait := make(chan struct{})
		gomock.InOrder(
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_Handshake_{
					Handshake: &dag_pb.UploadDagsRequest_Handshake{
						Namespace: &object_pb.Namespace{
							InstanceName:    "hello/world",
							ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
						},
						MaximumUnfinalizedParentsLimit: &object_pb.Limit{
							Count:     0,
							SizeBytes: 0,
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_InitiateDag_{
					InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
						RootReference: []byte{
							// SHA-256 hash.
							0x6a, 0x91, 0xfa, 0x7c, 0xe8, 0xf8, 0x87, 0xce,
							0xf3, 0x28, 0xb3, 0xf6, 0xc5, 0x48, 0x9d, 0xb7,
							0xd4, 0x00, 0x95, 0xae, 0x39, 0xa8, 0x08, 0x9a,
							0x6d, 0x7d, 0x68, 0x62, 0xb3, 0x4d, 0xe9, 0x4c,
							// Size in bytes.
							0x2a, 0x00, 0x00,
							// Height.
							0x00,
							// Degree.
							0x00, 0x00,
							// Maximum total parents size in bytes.
							0x00, 0x00,
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait }).
				Return(nil, io.EOF),
		)
		gomock.InOrder(
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_Handshake_{
					Handshake: &dag_pb.UploadDagsResponse_Handshake{
						MaximumUnfinalizedDagsCount: 5,
					},
				},
			})),
		)
		gomock.InOrder(
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "6a91fa7ce8f887cef328b3f6c5489db7d40095ae39a8089a6d7d6862b34de94c", 42, 0, 0, 0), nil, nil, false).
				Do(func(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases []any, wantContentsIfIncomplete bool) {
					close(wait)
					<-ctx.Done()
				}).
				Return(nil, status.Error(codes.Canceled, "Request canceled")),
		)

		testutil.RequireEqualStatus(
			t,
			status.Error(codes.InvalidArgument, "Client closed the request, even though the server still needs to request 1 or more objects"),
			uploaderServer.UploadDags(server),
		)
	})

	t.Run("PrematureCloseAfterRequestObject", func(t *testing.T) {
		// Similar to the above, the client may not close the
		// stream for sending if the server is waiting for the
		// client to send one or more ProvideObjectContents
		// messages.
		server := NewMockUploader_UploadDagsServer(ctrl)
		server.EXPECT().Context().Return(ctx).AnyTimes()
		wait := make(chan struct{})
		gomock.InOrder(
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_Handshake_{
					Handshake: &dag_pb.UploadDagsRequest_Handshake{
						Namespace: &object_pb.Namespace{
							InstanceName:    "hello/world",
							ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
						},
						MaximumUnfinalizedParentsLimit: &object_pb.Limit{
							Count:     0,
							SizeBytes: 0,
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().Return(&dag_pb.UploadDagsRequest{
				Type: &dag_pb.UploadDagsRequest_InitiateDag_{
					InitiateDag: &dag_pb.UploadDagsRequest_InitiateDag{
						RootReference: []byte{
							// SHA-256 hash.
							0xbd, 0x42, 0x35, 0xa5, 0xb1, 0x12, 0xd4, 0x60,
							0x49, 0x1e, 0xaa, 0x7f, 0xd6, 0xcd, 0xce, 0x3c,
							0xd5, 0x8e, 0xfe, 0x4d, 0x9d, 0x5c, 0xe5, 0xb4,
							0xde, 0x75, 0xb7, 0x2f, 0x73, 0x92, 0xa1, 0x2e,
							// Size in bytes.
							0x2a, 0x00, 0x00,
							// Height.
							0x00,
							// Degree.
							0x00, 0x00,
							// Maximum total parents size in bytes.
							0x00, 0x00,
						},
					},
				},
			}, nil),
			server.EXPECT().Recv().
				Do(func() { <-wait }).
				Return(nil, io.EOF),
		)
		gomock.InOrder(
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_Handshake_{
					Handshake: &dag_pb.UploadDagsResponse_Handshake{
						MaximumUnfinalizedDagsCount: 5,
					},
				},
			})),
			server.EXPECT().Send(testutil.EqProto(t, &dag_pb.UploadDagsResponse{
				Type: &dag_pb.UploadDagsResponse_RequestObject_{
					RequestObject: &dag_pb.UploadDagsResponse_RequestObject{
						LowestReferenceIndex: 0,
						RequestContents:      true,
					},
				},
			})).Do(func(response *dag_pb.UploadDagsResponse) { close(wait) }),
		)
		gomock.InOrder(
			objectUploader.EXPECT().
				UploadObject(gomock.Any(), object.MustNewSHA256V1GlobalReference("hello/world", "bd4235a5b112d460491eaa7fd6cdce3cd58efe4d9d5ce5b4de75b72f7392a12e", 42, 0, 0, 0), nil, nil, false).
				Return(object.UploadObjectMissing[any]{}, nil),
		)

		testutil.RequireEqualStatus(
			t,
			status.Error(codes.InvalidArgument, "Client closed the request, even though the client still needs to provide the contents of 1 or more objects"),
			uploaderServer.UploadDags(server),
		)
	})
}
