package evaluation_test

import (
	"context"
	"errors"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_evaluation_cache_pb "bonanza.build/pkg/proto/model/evaluation/cache"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
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
		tagResolver := NewMockBoundResolverForTesting(ctrl)
		tagResolver.EXPECT().ResolveTag(gomock.Any(), gomock.Any()).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
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
		tagResolver := NewMockBoundResolverForTesting(ctrl)
		tagResolver.EXPECT().ResolveTag(gomock.Any(), gomock.Any()).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
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
		tagResolver := NewMockBoundResolverForTesting(ctrl)
		tagResolver.EXPECT().ResolveTag(gomock.Any(), gomock.Any()).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
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

	t.Run("CacheLookupNotFound", func(t *testing.T) {
		// For every key for which evaluation is requested, we
		// should first see a call to ResolveTag() to obtain the
		// set of expected dependencies. If this returns
		// NOT_FOUND, evaluation should be performed.
		computer := NewMockComputerForTesting(ctrl)
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagResolver := NewMockBoundResolverForTesting(ctrl)
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
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("10479a81dafa74a2f72438cbce7d472cb9e8ea3648a9a2ec3622a27620a02925", 23721, 0, 0, 0),
			cacheObjectReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)

		keyState, err := recursiveComputer.GetOrCreateKeyState(
			model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
				&wrapperspb.UInt32Value{
					Value: 67,
				},
			),
		)
		require.NoError(t, err)

		tagResolver.EXPECT().ResolveTag(
			gomock.Any(),
			[...]byte{
				0xbf, 0xba, 0xc9, 0x92, 0x7b, 0xff, 0x1f, 0xfe,
				0xd1, 0x95, 0x69, 0xab, 0x3a, 0x78, 0x6c, 0xf7,
				0x56, 0x95, 0x13, 0x19, 0x5f, 0xbe, 0x80, 0xc5,
				0x74, 0x76, 0x7c, 0x78, 0xea, 0x47, 0x94, 0xc6,
			},
		).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist"))
		computer.EXPECT().ComputeMessageValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata], error) {
				return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](
					&wrapperspb.UInt64Value{Value: 42},
				), nil
			})

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
			Value: 42,
		}, value.Message)
	})

	t.Run("CacheLookupReadNotFound", func(t *testing.T) {
		// Even though we require that tags of cached results
		// resolve to complete graphs, flaky storage may always
		// cause subsequent reads to return NOT_FOUND. In such
		// cases we simply assume there's a cache miss.
		computer := NewMockComputerForTesting(ctrl)
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagResolver := NewMockBoundResolverForTesting(ctrl)
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
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("0c1c0eacebd721476e1a158da2e3ee0281c9c6fe9e8f9e2941f2a05153869b32", 48374, 0, 0, 0),
			cacheObjectReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)

		keyState, err := recursiveComputer.GetOrCreateKeyState(
			model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
				&wrapperspb.UInt32Value{
					Value: 67,
				},
			),
		)
		require.NoError(t, err)

		tagResolver.EXPECT().ResolveTag(
			gomock.Any(),
			[...]byte{
				0x96, 0x82, 0x42, 0x1e, 0x5e, 0x3e, 0x71, 0x8b,
				0x56, 0x4f, 0x6a, 0xbe, 0x93, 0x0f, 0x90, 0x60,
				0x70, 0xf9, 0xef, 0xe8, 0x25, 0x70, 0x88, 0x90,
				0xc4, 0x9e, 0xed, 0x80, 0x6e, 0x92, 0xd1, 0x17,
			},
		).Return(object.MustNewSHA256V1LocalReference("12271f9d66852891725166bd460656b9a2b9a5c6697a51311963ac7d5acd2d0c", 48374, 0, 0, 0), nil)
		cacheObjectReader.EXPECT().ReadObject(
			gomock.Any(),
			util.Must(
				model_core.NewDecodable(
					object.MustNewSHA256V1LocalReference("12271f9d66852891725166bd460656b9a2b9a5c6697a51311963ac7d5acd2d0c", 48374, 0, 0, 0),
					[]byte{
						0x63, 0xa0, 0x43, 0x40, 0xfc, 0x0b, 0x58, 0x14,
						0x7f, 0x9d, 0x56, 0x84, 0x39, 0xff, 0x39, 0x12,
					},
				),
			),
		).Return(model_core.Message[*model_evaluation_cache_pb.LookupResult, object.LocalReference]{}, status.Error(codes.NotFound, "Tag does not exist"))
		computer.EXPECT().ComputeMessageValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata], error) {
				return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](
					&wrapperspb.UInt64Value{Value: 42},
				), nil
			})

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
			Value: 42,
		}, value.Message)
	})
}
