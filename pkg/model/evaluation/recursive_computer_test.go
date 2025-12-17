package evaluation_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_evaluation "bonanza.build/pkg/model/evaluation"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"go.uber.org/mock/gomock"
)

func TestRecursiveComputer(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	t.Run("Fibonacci", func(t *testing.T) {
		// Example usage, where we provide a very basic
		// implementation of Computer that attempts to compute
		// the Fibonacci sequence recursively. Due to
		// memoization, this should run in polynomial time.
		computer := NewMockComputerForTesting(ctrl)
		computer.EXPECT().ComputeMessageValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata], error) {
				// Base case: fib(0) and fib(1).
				k := key.Message.(*wrapperspb.UInt32Value)
				if k.Value <= 1 {
					return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](
						&wrapperspb.UInt64Value{
							Value: uint64(k.Value),
						},
					), nil
				}

				// Recursion: fib(n) = fib(n-2) + fib(n-1).
				v0 := e.GetMessageValue(model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](&wrapperspb.UInt32Value{
					Value: k.Value - 2,
				}))
				v1 := e.GetMessageValue(model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](&wrapperspb.UInt32Value{
					Value: k.Value - 1,
				}))
				if !v0.IsSet() || !v1.IsSet() {
					return model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata]{}, model_evaluation.ErrMissingDependency
				}
				return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](
					&wrapperspb.UInt64Value{
						Value: v0.Message.(*wrapperspb.UInt64Value).Value + v1.Message.(*wrapperspb.UInt64Value).Value,
					},
				), nil
			}).
			AnyTimes()
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagResolver := NewMockTagResolverForTesting(ctrl)
		tagResolver.EXPECT().ResolveTag(gomock.Any(), struct{}{}, gomock.Any(), nil).Return(tag.SignedValue{}, false, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
		cacheObjectReader := NewMockCacheObjectReaderForTesting(ctrl)
		cacheObjectReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		cacheDeterministicEncoder := NewMockDeterministicBinaryEncoder(ctrl)
		cacheKeyedEncoder := NewMockKeyedBinaryEncoder(ctrl)

		queuesFactory := model_evaluation.NewSimpleRecursiveComputerEvaluationQueuesFactory[object.LocalReference, model_core.ReferenceMetadata](1)
		queues := queuesFactory.NewQueues()
		recursiveComputer := model_evaluation.NewRecursiveComputer(
			computer,
			queues,
			object.SHA256V1ReferenceFormat,
			objectManager,
			tagResolver,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("f07997aa26d63ad33c8b2e6f920ae9b42c93bacb67c84ae529d065c6d572d342", 2323, 0, 0, 0),
			/* cacheTagSignaturePrivateKey = */ ed25519.PrivateKey{
				0x52, 0x4b, 0xc2, 0xe8, 0xae, 0x13, 0x6e, 0xca,
				0x02, 0x54, 0x40, 0xa6, 0xe2, 0xda, 0xed, 0xe5,
				0x69, 0xfc, 0x18, 0xec, 0x52, 0xbd, 0x9c, 0x4e,
				0x80, 0xf6, 0x61, 0x3f, 0x5a, 0x10, 0x25, 0x7c,
				0x8c, 0x2a, 0x9e, 0x03, 0x22, 0x9f, 0xa2, 0x1c,
				0x27, 0xf6, 0xd6, 0xcd, 0xc9, 0xa0, 0xc8, 0x4f,
				0x6c, 0x2f, 0xb5, 0x71, 0x8f, 0x15, 0xc1, 0x6b,
				0xe5, 0x33, 0xc0, 0xe8, 0x8c, 0xf8, 0x7a, 0x16,
			},
			cacheObjectReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)
		keyState, err := recursiveComputer.GetOrCreateKeyState(
			model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
				&wrapperspb.UInt32Value{
					Value: 93,
				},
			),
		)
		require.NoError(t, err)

		var value model_core.Message[proto.Message, object.LocalReference]
		require.NoError(
			t,
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)

				var err error
				value, err = recursiveComputer.WaitForMessageValue(ctx, keyState)
				return err
			}),
		)
		testutil.RequireEqualProto(t, &wrapperspb.UInt64Value{
			Value: 12200160415121876738,
		}, value.Message)
	})

	t.Run("Cycle", func(t *testing.T) {
		// Provide a computer that does nothing more than
		// request its own value recursively. This should
		// immediately trigger cycle detection.
		computer := NewMockComputerForTesting(ctrl)
		computer.EXPECT().ComputeMessageValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata], error) {
				v := e.GetMessageValue(model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata](key.Message))
				require.False(t, v.IsSet())
				return model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata]{}, model_evaluation.ErrMissingDependency
			})
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagResolver := NewMockTagResolverForTesting(ctrl)
		tagResolver.EXPECT().ResolveTag(gomock.Any(), struct{}{}, gomock.Any(), nil).Return(tag.SignedValue{}, false, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
		cacheObjectReader := NewMockCacheObjectReaderForTesting(ctrl)
		cacheObjectReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		cacheDeterministicEncoder := NewMockDeterministicBinaryEncoder(ctrl)
		cacheKeyedEncoder := NewMockKeyedBinaryEncoder(ctrl)

		queuesFactory := model_evaluation.NewSimpleRecursiveComputerEvaluationQueuesFactory[object.LocalReference, model_core.ReferenceMetadata](1)
		queues := queuesFactory.NewQueues()
		recursiveComputer := model_evaluation.NewRecursiveComputer(
			computer,
			queues,
			object.SHA256V1ReferenceFormat,
			objectManager,
			tagResolver,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("0e847672a7a34ba848ec92f4000a9f86049e5557496cfcede0db7744bf77c12b", 8575, 0, 0, 0),
			/* cacheTagSignaturePrivateKey = */ ed25519.PrivateKey{
				0x4f, 0x56, 0x01, 0xdd, 0x16, 0x90, 0x1d, 0xdc,
				0xea, 0x7b, 0x77, 0x9b, 0x21, 0x71, 0x9c, 0x9b,
				0x20, 0x4a, 0x1d, 0x26, 0x8e, 0x26, 0x35, 0xd6,
				0x09, 0x3c, 0xdb, 0x2f, 0x2b, 0x90, 0xbb, 0x06,
				0x36, 0x62, 0xd1, 0xa5, 0x49, 0x75, 0xb9, 0xb5,
				0x91, 0x86, 0x5d, 0xb3, 0x66, 0x1e, 0x14, 0xed,
				0x0f, 0xd3, 0x33, 0x31, 0x3d, 0xae, 0x13, 0x39,
				0xd1, 0xe2, 0x65, 0x1c, 0x50, 0xdf, 0x19, 0xb0,
			},
			cacheObjectReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)
		keyState, err := recursiveComputer.GetOrCreateKeyState(
			model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
				&wrapperspb.UInt32Value{
					Value: 42,
				},
			),
		)
		require.NoError(t, err)

		require.EqualExportedValues(
			t,
			model_evaluation.NestedError[object.LocalReference]{
				Key: model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
					&wrapperspb.UInt32Value{
						Value: 42,
					},
				),
				Err: errors.New("cyclic evaluation detected"),
			},
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)

				_, err := recursiveComputer.WaitForMessageValue(ctx, keyState)
				return err
			}),
		)
	})

	t.Run("ErrorPropagation", func(t *testing.T) {
		// Have a simple function that calls a couple of levels
		// deep. At some point this will return an error, which
		// should be propagated all the way back up.
		computer := NewMockComputerForTesting(ctrl)
		computer.EXPECT().ComputeMessageValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata], error) {
				k := key.Message.(*wrapperspb.UInt32Value)
				if k.Value == 0 {
					return model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata]{}, errors.New("reached zero")
				}

				v := e.GetMessageValue(model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](&wrapperspb.UInt32Value{
					Value: k.Value - 1,
				}))
				if !v.IsSet() {
					return model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata]{}, model_evaluation.ErrMissingDependency
				}
				return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](
					&emptypb.Empty{},
				), nil
			}).
			Times(4)
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagResolver := NewMockTagResolverForTesting(ctrl)
		tagResolver.EXPECT().ResolveTag(gomock.Any(), struct{}{}, gomock.Any(), nil).Return(tag.SignedValue{}, false, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
		cacheObjectReader := NewMockCacheObjectReaderForTesting(ctrl)
		cacheObjectReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		cacheDeterministicEncoder := NewMockDeterministicBinaryEncoder(ctrl)
		cacheKeyedEncoder := NewMockKeyedBinaryEncoder(ctrl)

		queuesFactory := model_evaluation.NewSimpleRecursiveComputerEvaluationQueuesFactory[object.LocalReference, model_core.ReferenceMetadata](1)
		queues := queuesFactory.NewQueues()
		recursiveComputer := model_evaluation.NewRecursiveComputer(
			computer,
			queues,
			object.SHA256V1ReferenceFormat,
			objectManager,
			tagResolver,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("e5283197708f96f2368701a89fcdd72367106497f0335bd2d5f3403a826d71da", 8584, 0, 0, 0),
			/* cacheTagSignaturePrivateKey = */ ed25519.PrivateKey{
				0x23, 0xc0, 0xb9, 0x12, 0x3d, 0x8b, 0xbd, 0xb7,
				0x89, 0x78, 0xde, 0xd8, 0x71, 0x4a, 0x9c, 0x19,
				0xd8, 0x0a, 0x6f, 0xab, 0xa6, 0xc1, 0x00, 0xbc,
				0x88, 0xb1, 0x98, 0x38, 0x78, 0x8a, 0x1b, 0x9f,
				0x75, 0x34, 0x82, 0xae, 0x7d, 0x32, 0xad, 0x0c,
				0x0f, 0xcb, 0xbe, 0xd8, 0x65, 0x3c, 0x03, 0x79,
				0xb7, 0x1f, 0xb0, 0xa3, 0x0f, 0xb9, 0xb4, 0x5c,
				0xb3, 0x6e, 0x52, 0x02, 0xe3, 0x7d, 0x83, 0x80,
			},
			cacheObjectReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)

		keyState2, err := recursiveComputer.GetOrCreateKeyState(
			model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
				&wrapperspb.UInt32Value{
					Value: 2,
				},
			),
		)
		require.NoError(t, err)
		require.EqualExportedValues(
			t,
			model_evaluation.NestedError[object.LocalReference]{
				Key: model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
					&wrapperspb.UInt32Value{
						Value: 1,
					},
				),
				Err: model_evaluation.NestedError[object.LocalReference]{
					Key: model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 0,
						},
					),
					Err: errors.New("reached zero"),
				},
			},
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)

				_, err := recursiveComputer.WaitForMessageValue(ctx, keyState2)
				return err
			}),
		)

		// A subsequent evaluation of a new key that depends on
		// a previously computed key that is already in the
		// error state should also propagate the error.
		keyState3, err := recursiveComputer.GetOrCreateKeyState(
			model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
				&wrapperspb.UInt32Value{
					Value: 3,
				},
			),
		)
		require.NoError(t, err)
		require.EqualExportedValues(
			t,
			model_evaluation.NestedError[object.LocalReference]{
				Key: model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
					&wrapperspb.UInt32Value{
						Value: 2,
					},
				),
				Err: model_evaluation.NestedError[object.LocalReference]{
					Key: model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 1,
						},
					),
					Err: model_evaluation.NestedError[object.LocalReference]{
						Key: model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
							&wrapperspb.UInt32Value{
								Value: 0,
							},
						),
						Err: errors.New("reached zero"),
					},
				},
			},
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)

				_, err := recursiveComputer.WaitForMessageValue(ctx, keyState3)
				return err
			}),
		)
	})
}
