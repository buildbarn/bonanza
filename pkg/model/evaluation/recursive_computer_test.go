package evaluation_test

import (
	"context"
	"errors"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
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
	"google.golang.org/protobuf/types/known/anypb"
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
		lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
		lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		keysReader := NewMockKeysReaderForTesting(ctrl)
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
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)
		keyState, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 93,
						},
					),
				),
			),
		)
		require.NoError(t, err)

		require.NoError(
			t,
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
				return recursiveComputer.WaitForEvaluation(ctx, keyState)
			}),
		)
		/*
			testutil.RequireEqualProto(t, &wrapperspb.UInt64Value{
				Value: 12200160415121876738,
			}, value.Message)
		*/
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
		lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
		lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		keysReader := NewMockKeysReaderForTesting(ctrl)
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
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)
		keyState, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 42,
						},
					),
				),
			),
		)
		require.NoError(t, err)

		require.Equal(
			t,
			model_evaluation.NestedError[object.LocalReference, model_core.ReferenceMetadata]{
				KeyState: keyState,
				Err:      errors.New("cyclic dependency detected"),
			},
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
				return recursiveComputer.WaitForEvaluation(ctx, keyState)
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
			Times(6)
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagResolver := NewMockBoundResolverForTesting(ctrl)
		tagResolver.EXPECT().ResolveTag(gomock.Any(), gomock.Any()).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
		lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
		lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		keysReader := NewMockKeysReaderForTesting(ctrl)
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
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)

		keyState2, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 2,
						},
					),
				),
			),
		)
		require.NoError(t, err)
		errCompute := program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
			return recursiveComputer.WaitForEvaluation(ctx, keyState2)
		})

		keyState0, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 0,
						},
					),
				),
			),
		)
		require.NoError(t, err)
		keyState1, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 1,
						},
					),
				),
			),
		)
		require.NoError(t, err)

		require.Equal(
			t,
			model_evaluation.NestedError[object.LocalReference, model_core.ReferenceMetadata]{
				KeyState: keyState1,
				Err: model_evaluation.NestedError[object.LocalReference, model_core.ReferenceMetadata]{
					KeyState: keyState0,
					Err:      errors.New("reached zero"),
				},
			},
			errCompute,
		)

		// A subsequent evaluation of a new key that depends on
		// a previously computed key that is already in the
		// error state should also propagate the error.
		keyState3, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 3,
						},
					),
				),
			),
		)
		require.NoError(t, err)
		require.Equal(
			t,
			model_evaluation.NestedError[object.LocalReference, model_core.ReferenceMetadata]{
				KeyState: keyState2,
				Err: model_evaluation.NestedError[object.LocalReference, model_core.ReferenceMetadata]{
					KeyState: keyState1,
					Err: model_evaluation.NestedError[object.LocalReference, model_core.ReferenceMetadata]{
						KeyState: keyState0,
						Err:      errors.New("reached zero"),
					},
				},
			},
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
				return recursiveComputer.WaitForEvaluation(ctx, keyState3)
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
		lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
		lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		keysReader := NewMockKeysReaderForTesting(ctrl)
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
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)

		keyState, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 67,
						},
					),
				),
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

		require.NoError(
			t,
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
				return recursiveComputer.WaitForEvaluation(ctx, keyState)
			}),
		)
		/*
			testutil.RequireEqualProto(t, &wrapperspb.UInt64Value{
				Value: 42,
			}, value.Message)
		*/
	})

	t.Run("CacheLookupReadNotFound", func(t *testing.T) {
		// Even though we require that tags of cached results
		// resolve to complete graphs, flaky storage may always
		// cause subsequent reads to return NOT_FOUND. In such
		// cases we simply assume there's a cache miss.
		computer := NewMockComputerForTesting(ctrl)
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagResolver := NewMockBoundResolverForTesting(ctrl)
		lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
		lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		keysReader := NewMockKeysReaderForTesting(ctrl)
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
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)

		keyState, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 67,
						},
					),
				),
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
		lookupResultReader.EXPECT().ReadObject(
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
		).Return(model_core.Message[*model_evaluation_cache_pb.LookupResult, object.LocalReference]{}, status.Error(codes.NotFound, "Object does not exist"))
		computer.EXPECT().ComputeMessageValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata], error) {
				return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](
					&wrapperspb.UInt64Value{Value: 42},
				), nil
			})

		require.NoError(
			t,
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
				return recursiveComputer.WaitForEvaluation(ctx, keyState)
			}),
		)
		/*
			testutil.RequireEqualProto(t, &wrapperspb.UInt64Value{
				Value: 42,
			}, value.Message)
		*/
	})

	t.Run("CacheLookupReturningUnexpectedResult", func(t *testing.T) {
		// The first cache lookup should typically contain an
		// Initial message, describing the initial set of
		// dependencies that need to be considered. If the
		// message is malformed and contains another type of
		// message, we should just ignore it.
		computer := NewMockComputerForTesting(ctrl)
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagResolver := NewMockBoundResolverForTesting(ctrl)
		lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
		lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		keysReader := NewMockKeysReaderForTesting(ctrl)
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
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("4466f601ba900ce4e0d606dc68dd0c35c5e976792784400514f42905d6deba6e", 64722, 0, 0, 0),
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)

		keyState, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 12,
						},
					),
				),
			),
		)
		require.NoError(t, err)

		tagResolver.EXPECT().ResolveTag(
			gomock.Any(),
			[...]byte{
				0x57, 0x12, 0x01, 0xf0, 0x80, 0xf7, 0xe1, 0x9f,
				0x41, 0x44, 0x87, 0x88, 0x9b, 0xf3, 0xc9, 0x4f,
				0x61, 0x68, 0x71, 0xd5, 0x07, 0x06, 0x3e, 0x92,
				0xdc, 0x40, 0x42, 0xbf, 0x2a, 0x3c, 0x01, 0xec,
			},
		).Return(object.MustNewSHA256V1LocalReference("829d56c5c910ff51bb09192f5655cd51c02e54c3e16b00d2fde8a6b7e3d814fc", 95938, 0, 0, 0), nil)
		lookupResultReader.EXPECT().ReadObject(
			gomock.Any(),
			util.Must(
				model_core.NewDecodable(
					object.MustNewSHA256V1LocalReference("829d56c5c910ff51bb09192f5655cd51c02e54c3e16b00d2fde8a6b7e3d814fc", 95938, 0, 0, 0),
					[]byte{
						0x9c, 0x80, 0x78, 0x1a, 0xa4, 0x29, 0x20, 0x6d,
						0x76, 0xa4, 0x4e, 0x8f, 0x4c, 0x2e, 0xce, 0xd5,
					},
				),
			),
		).Return(model_core.NewSimpleMessage[object.LocalReference](
			&model_evaluation_cache_pb.LookupResult{
				Result: &model_evaluation_cache_pb.LookupResult_MissingDependencies_{
					MissingDependencies: &model_evaluation_cache_pb.LookupResult_MissingDependencies{},
				},
			},
		), nil)
		computer.EXPECT().ComputeMessageValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata], error) {
				return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](
					&wrapperspb.UInt64Value{Value: 13},
				), nil
			})

		require.NoError(
			t,
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
				return recursiveComputer.WaitForEvaluation(ctx, keyState)
			}),
		)
		/*
			testutil.RequireEqualProto(t, &wrapperspb.UInt64Value{
				Value: 13,
			}, value.Message)
		*/
	})

	t.Run("CacheLookupWithoutDependencies", func(t *testing.T) {
		// In the extremely rare case that a key has no
		// dependencies, the initial lookup result may a value
		// directly. There should be no need to call into the
		// computer, as the cached value can be used.
		computer := NewMockComputerForTesting(ctrl)
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagResolver := NewMockBoundResolverForTesting(ctrl)
		lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
		lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		keysReader := NewMockKeysReaderForTesting(ctrl)
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
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("2fbd13f1ee48876ec1e85961b59877c3916fb10ae23e7e1e45083feecafe1804", 57483, 0, 0, 0),
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)

		keyState, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 24,
						},
					),
				),
			),
		)
		require.NoError(t, err)

		tagResolver.EXPECT().ResolveTag(
			gomock.Any(),
			[...]byte{
				0x1e, 0x0f, 0x30, 0xc1, 0x1c, 0x0a, 0x4d, 0x1c,
				0xd6, 0x18, 0x13, 0x8d, 0xa3, 0x58, 0x40, 0xfa,
				0x31, 0x22, 0x07, 0x66, 0x38, 0x45, 0x22, 0x9b,
				0x70, 0x31, 0xbb, 0xac, 0xe8, 0x70, 0x35, 0x07,
			},
		).Return(object.MustNewSHA256V1LocalReference("e1749fb7b82448ae6941d4f7e515818efbf781e6fe2738465bd9ce876b8d9866", 2345, 0, 0, 0), nil)
		lookupResultReader.EXPECT().ReadObject(
			gomock.Any(),
			util.Must(
				model_core.NewDecodable(
					object.MustNewSHA256V1LocalReference("e1749fb7b82448ae6941d4f7e515818efbf781e6fe2738465bd9ce876b8d9866", 2345, 0, 0, 0),
					[]byte{
						0xe5, 0x83, 0xfe, 0x6a, 0x73, 0xa3, 0x7f, 0xe9,
						0x5c, 0xb8, 0x4f, 0x3a, 0x54, 0xdf, 0xd3, 0x87,
					},
				),
			),
		).Return(model_core.NewSimpleMessage[object.LocalReference](
			&model_evaluation_cache_pb.LookupResult{
				Result: &model_evaluation_cache_pb.LookupResult_HitValue{
					HitValue: &model_core_pb.Any{
						Value: util.Must(anypb.New(&wrapperspb.UInt64Value{
							Value: 26,
						})),
						References: &model_core_pb.ReferenceSet{},
					},
				},
			},
		), nil)

		require.NoError(
			t,
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
				return recursiveComputer.WaitForEvaluation(ctx, keyState)
			}),
		)
		/*
			testutil.RequireEqualProto(t, &wrapperspb.UInt64Value{
				Value: 26,
			}, value.Message)
		*/
	})

	t.Run("CacheLookupFoo", func(t *testing.T) {
		computer := NewMockComputerForTesting(ctrl)
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagResolver := NewMockBoundResolverForTesting(ctrl)
		lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
		lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		keysReader := NewMockKeysReaderForTesting(ctrl)
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
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("bf1b2cdd5f58461827eb9285ee37dd45fea02ebc4e278f28678ea2722f590337", 57483, 0, 0, 0),
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
		)

		keyState, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 18,
						},
					),
				),
			),
		)
		require.NoError(t, err)

		// Initial lookup for &wrapperspb.UInt32Value{Value: 18}.
		tagResolver.EXPECT().ResolveTag(
			gomock.Any(),
			[...]byte{
				0xba, 0x4e, 0xe4, 0x47, 0x51, 0x09, 0x77, 0x2d,
				0x0f, 0x8f, 0x90, 0x9d, 0xce, 0xc6, 0xe2, 0x14,
				0x6a, 0xee, 0xed, 0x9c, 0x1d, 0x44, 0x9d, 0x3f,
				0xd7, 0x49, 0x74, 0xf1, 0x93, 0xad, 0xb0, 0x52,
			},
		).Return(object.MustNewSHA256V1LocalReference("cd897b5bbc720746ba0740100714e8f98f36e3c3cda42b3d3e8c0c26884c0780", 43984, 0, 0, 0), nil)
		lookupResultReader.EXPECT().ReadObject(
			gomock.Any(),
			util.Must(
				model_core.NewDecodable(
					object.MustNewSHA256V1LocalReference("cd897b5bbc720746ba0740100714e8f98f36e3c3cda42b3d3e8c0c26884c0780", 43984, 0, 0, 0),
					[]byte{
						0xf9, 0x5c, 0x1b, 0x38, 0xaa, 0xc0, 0x11, 0x82,
						0x6e, 0x8c, 0xda, 0x68, 0x47, 0x16, 0x03, 0x48,
					},
				),
			),
		).Return(model_core.NewSimpleMessage[object.LocalReference](
			&model_evaluation_cache_pb.LookupResult{
				Result: &model_evaluation_cache_pb.LookupResult_Initial_{
					Initial: &model_evaluation_cache_pb.LookupResult_Initial{
						GraphletDependencyKeys: []*model_evaluation_pb.Keys{{
							Level: &model_evaluation_pb.Keys_Leaf{
								Leaf: &model_core_pb.Any{
									Value: util.Must(anypb.New(&wrapperspb.UInt32Value{
										Value: 16,
									})),
									References: &model_core_pb.ReferenceSet{},
								},
							},
						}},
						ValueDependencyKeys: []*model_evaluation_pb.Keys{
							{
								Level: &model_evaluation_pb.Keys_Leaf{
									Leaf: &model_core_pb.Any{
										Value: util.Must(anypb.New(&wrapperspb.UInt32Value{
											Value: 16,
										})),
										References: &model_core_pb.ReferenceSet{},
									},
								},
							},
							{
								Level: &model_evaluation_pb.Keys_Leaf{
									Leaf: &model_core_pb.Any{
										Value: util.Must(anypb.New(&wrapperspb.UInt32Value{
											Value: 17,
										})),
										References: &model_core_pb.ReferenceSet{},
									},
								},
							},
						},
					},
				},
			},
		), nil)

		// Initial lookup for &wrapperspb.UInt32Value{Value: 16}.
		tagResolver.EXPECT().ResolveTag(
			gomock.Any(),
			[...]byte{
				0x5e, 0x5b, 0x0e, 0xc1, 0x14, 0x87, 0x2d, 0x85,
				0x6c, 0xdc, 0x77, 0xfb, 0x44, 0x11, 0xb0, 0x8e,
				0xcd, 0xe6, 0x1e, 0x02, 0x04, 0x75, 0xeb, 0xc3,
				0xe0, 0x4a, 0x14, 0xfd, 0x17, 0xee, 0x87, 0x06,
			},
		).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
		computer.EXPECT().ComputeMessageValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata], error) {
				testutil.RequireEqualProto(t, &wrapperspb.UInt32Value{
					Value: 16,
				}, key.Message)
				return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](
					&wrapperspb.UInt32Value{
						Value: 987,
					},
				), nil
			})

		// Subsequent lookup for &wrapperspb.UInt32Value{Value: 18}.
		tagResolver.EXPECT().ResolveTag(
			gomock.Any(),
			[...]byte{
				0xba, 0x4e, 0xe4, 0x47, 0x51, 0x09, 0x77, 0x2d,
				0x0f, 0x8f, 0x90, 0x9d, 0xce, 0xc6, 0xe2, 0x14,
				0x6a, 0xee, 0xed, 0x9c, 0x1d, 0x44, 0x9d, 0x3f,
				0xd7, 0x49, 0x74, 0xf1, 0x93, 0xad, 0xb0, 0x52,
			},
		).Return(object.MustNewSHA256V1LocalReference("423b895a5498a677b5075170e6da337499e7f69646d50adabe4ba2026ecee247", 84733, 0, 0, 0), nil)
		lookupResultReader.EXPECT().ReadObject(
			gomock.Any(),
			util.Must(
				model_core.NewDecodable(
					object.MustNewSHA256V1LocalReference("423b895a5498a677b5075170e6da337499e7f69646d50adabe4ba2026ecee247", 84733, 0, 0, 0),
					[]byte{
						0xf9, 0x5c, 0x1b, 0x38, 0xaa, 0xc0, 0x11, 0x82,
						0x6e, 0x8c, 0xda, 0x68, 0x47, 0x16, 0x03, 0x48,
					},
				),
			),
		).Return(model_core.NewSimpleMessage[object.LocalReference](
			&model_evaluation_cache_pb.LookupResult{
				Result: &model_evaluation_cache_pb.LookupResult_HitValue{
					HitValue: &model_core_pb.Any{
						Value: util.Must(anypb.New(&wrapperspb.UInt64Value{
							Value: 2584,
						})),
						References: &model_core_pb.ReferenceSet{},
					},
				},
			},
		), nil)

		require.NoError(
			t,
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
				return recursiveComputer.WaitForEvaluation(ctx, keyState)
			}),
		)
		/*
			testutil.RequireEqualProto(t, &wrapperspb.UInt64Value{
				Value: 26,
			}, value.Message)
		*/
	})
}
