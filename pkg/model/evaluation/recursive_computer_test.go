package evaluation_test

import (
	"bytes"
	"context"
	"encoding"
	"errors"
	"testing"
	"time"

	"bonanza.build/pkg/model/chunking"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_format "bonanza.build/pkg/model/format"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
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

// Unmarshalers that are provided to Environment.GetValue() to
// unmarshal values used by these tests.
var (
	uint32ValueUnmarshaler = model_format.EraseUnmarshaler(model_format.NewProto[wrapperspb.UInt32Value]())
	uint64ValueUnmarshaler = model_format.EraseUnmarshaler(model_format.NewProto[wrapperspb.UInt64Value]())
)

// protoValue converts a Protobuf message to a value that can be
// returned by Computer.ComputeValue().
func protoValue(m proto.Message) model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata] {
	return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata](
		model_format.ProtoMarshaler[proto.Message]{}.NewMarshaler(m),
	)
}

// testChunkingParameters are the chunking parameters that are used by
// RecursiveComputer in these tests.
var testChunkingParameters = util.Must(chunking.NewParametersFromProto(
	model_chunking_pb.Parameters_builder{
		MinimumSizeBytes: 1 << 16,
		HorizonSizeBytes: 8 << 16,
	}.Build(),
	object.SHA256V1ReferenceFormat,
))

func TestRecursiveComputer(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	t.Run("Fibonacci", func(t *testing.T) {
		// Example usage, where we provide a very basic
		// implementation of Computer that attempts to compute
		// the Fibonacci sequence recursively. Due to
		// memoization, this should run in polynomial time.
		computer := NewMockComputerForTesting(ctrl)
		computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
				// Base case: fib(0) and fib(1).
				k := key.Message.(*wrapperspb.UInt32Value)
				if k.Value <= 1 {
					return protoValue(
						&wrapperspb.UInt64Value{
							Value: uint64(k.Value),
						},
					), nil
				}

				// Recursion: fib(n) = fib(n-2) + fib(n-1).
				v0 := e.GetValue(model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](&wrapperspb.UInt32Value{
					Value: k.Value - 2,
				}), uint64ValueUnmarshaler)
				v1 := e.GetValue(model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](&wrapperspb.UInt32Value{
					Value: k.Value - 1,
				}), uint64ValueUnmarshaler)
				if !v0.IsSet() || !v1.IsSet() {
					return model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata]{}, model_evaluation.ErrMissingDependency
				}
				return protoValue(
					&wrapperspb.UInt64Value{
						Value: v0.Message.(*wrapperspb.UInt64Value).Value + v1.Message.(*wrapperspb.UInt64Value).Value,
					},
				), nil
			}).
			AnyTimes()
		computer.EXPECT().IsLookup("type.googleapis.com/google.protobuf.UInt32Value").Return(false).AnyTimes()
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagStore := NewMockBoundStoreForTesting(ctrl)
		tagStore.EXPECT().ResolveTag(gomock.Any(), gomock.Any()).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
		evaluationReader := NewMockProtoEvaluationReaderForTesting(ctrl)
		lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
		lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		keysReader := NewMockKeysReaderForTesting(ctrl)
		cacheDeterministicEncoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
		cacheKeyedEncoder := NewMockKeyedBinaryEncoder(ctrl)

		queuesFactory := model_evaluation.NewSimpleRecursiveComputerEvaluationQueuesFactory[object.LocalReference, model_core.ReferenceMetadata](1)
		queues := queuesFactory.NewQueues()
		recursiveComputer := model_evaluation.NewRecursiveComputer(
			computer,
			queues,
			object.SHA256V1ReferenceFormat,
			testChunkingParameters,
			objectManager,
			tagStore,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("f07997aa26d63ad33c8b2e6f920ae9b42c93bacb67c84ae529d065c6d572d342", 2323, 0, 0, 0),
			evaluationReader,
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
			/* cacheLookupResultIndirectionMaximumAge = */ 24*time.Hour,
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

		objectManager.EXPECT().CaptureCreatedObject(gomock.Any(), gomock.Any()).
			Return(model_core.NoopReferenceMetadata{}, nil).
			AnyTimes()
		objectManager.EXPECT().CaptureExistingObject(gomock.Any()).
			Return(model_core.NoopReferenceMetadata{}).
			AnyTimes()
		objectManager.EXPECT().ReferenceObject(gomock.Any()).
			DoAndReturn(func(metadataEntry model_core.MetadataEntry[model_core.ReferenceMetadata]) object.LocalReference {
				return metadataEntry.LocalReference
			}).
			AnyTimes()

		patchedEvaluations, err := recursiveComputer.GetEvaluations(
			ctx,
			[]*model_evaluation.KeyState[object.LocalReference, model_core.ReferenceMetadata]{
				keyState,
			},
		)
		require.NoError(t, err)

		// As the requested key does not depend on any keys for
		// which overrides are provided, its value is constant.
		// It should nevertheless be part of the evaluations, as
		// the caller needs to be able to obtain its value. The
		// key itself is not embedded, as the caller already
		// knows it.
		evaluations, _ := patchedEvaluations.SortAndSetReferences()
		require.Len(t, evaluations.Message, 1)
		testutil.RequireEqualProto(t, &model_evaluation_pb.Evaluations{
			Level: &model_evaluation_pb.Evaluations_Leaf_{Leaf: &model_evaluation_pb.Evaluations_Leaf{
				// &wrapperspb.UInt32Value{Value: 93}
				KeyReference: []byte{
					0x0c, 0xfe, 0x3c, 0x1c, 0x36, 0x0e, 0x96, 0x44,
					0x16, 0xc0, 0x88, 0xf3, 0xfb, 0x93, 0xd4, 0x68,
					0xbe, 0x6a, 0xfd, 0xe0, 0x35, 0xc4, 0x8e, 0xf4,
					0x41, 0xe7, 0x61, 0x11, 0xa8, 0x1b, 0xe3, 0xdd,
					0x35, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				},
				Graphlet: &model_evaluation_pb.Graphlet{
					Evaluation: &model_evaluation_pb.Graphlet_EvaluationInline{EvaluationInline: &model_evaluation_pb.Evaluation{
						Value: model_core_pb.Opaque_builder{
							Value: util.Must(proto.Marshal(&wrapperspb.UInt64Value{
								Value: 12200160415121876738,
							})),
							References: &model_core_pb.ReferenceSet{},
						}.Build(),
					}},
				},
			}},
		}, evaluations.Message[0])
	})

	t.Run("ReportProgress", func(t *testing.T) {
		for name, reportedValue := range map[string]string{
			"NonEmpty": "Halfway there",
			// Progress messages whose fields all have
			// default values marshal to an empty byte
			// sequence.
			"Empty": "",
		} {
			t.Run(name, func(t *testing.T) {
				// Progress reported by the evaluation function should
				// be returned by GetProgress() while the key is being
				// evaluated, and be discarded afterwards.
				progressReported := make(chan struct{})
				finishEvaluation := make(chan struct{})
				computer := NewMockComputerForTesting(ctrl)
				computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
						e.ReportProgress(&wrapperspb.StringValue{Value: reportedValue})
						close(progressReported)
						<-finishEvaluation
						return protoValue(&wrapperspb.UInt64Value{Value: 42}), nil
					})
				computer.EXPECT().IsLookup("type.googleapis.com/google.protobuf.UInt32Value").Return(false).AnyTimes()
				objectManager := NewMockObjectManagerForTesting(ctrl)
				tagStore := NewMockBoundStoreForTesting(ctrl)
				tagStore.EXPECT().ResolveTag(gomock.Any(), gomock.Any()).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
				lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
				lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()

				queuesFactory := model_evaluation.NewSimpleRecursiveComputerEvaluationQueuesFactory[object.LocalReference, model_core.ReferenceMetadata](1)
				queues := queuesFactory.NewQueues()
				recursiveComputer := model_evaluation.NewRecursiveComputer(
					computer,
					queues,
					object.SHA256V1ReferenceFormat,
					testChunkingParameters,
					objectManager,
					tagStore,
					/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("f07997aa26d63ad33c8b2e6f920ae9b42c93bacb67c84ae529d065c6d572d342", 2323, 0, 0, 0),
					NewMockProtoEvaluationReaderForTesting(ctrl),
					lookupResultReader,
					NewMockKeysReaderForTesting(ctrl),
					model_encoding.NewChainedDeterministicBinaryEncoder(nil),
					NewMockKeyedBinaryEncoder(ctrl),
					clock.SystemClock,
					/* cacheLookupResultIndirectionMaximumAge = */ 24*time.Hour,
				)

				keyState, err := recursiveComputer.GetOrCreateKeyState(
					util.Must(
						model_core.MarshalTopLevelAny(
							model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
								&wrapperspb.UInt32Value{Value: 7},
							),
						),
					),
				)
				require.NoError(t, err)

				require.NoError(
					t,
					program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
						queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)

						<-progressReported
						progress, err := recursiveComputer.GetProgress(ctx)
						require.NoError(t, err)
						require.Len(t, progress.Message.GetOldestEvaluatingKeys(), 1)
						// Progress messages that marshal to an
						// empty byte sequence should still be
						// sent to the client.
						marshaledProgress, err := proto.Marshal(progress.Message)
						require.NoError(t, err)
						var receivedProgress model_evaluation_pb.Progress
						require.NoError(t, proto.Unmarshal(marshaledProgress, &receivedProgress))
						require.Len(t, receivedProgress.GetOldestEvaluatingKeys(), 1)
						require.NotNil(t, receivedProgress.GetOldestEvaluatingKeys()[0].GetProgress())
						var gotProgress wrapperspb.StringValue
						require.NoError(t, proto.Unmarshal(receivedProgress.GetOldestEvaluatingKeys()[0].GetProgress(), &gotProgress))
						testutil.RequireEqualProto(t, &wrapperspb.StringValue{Value: reportedValue}, &gotProgress)
						progress.Discard()

						close(finishEvaluation)
						if err := recursiveComputer.WaitForEvaluation(ctx, keyState); err != nil {
							return err
						}

						progress, err = recursiveComputer.GetProgress(ctx)
						require.NoError(t, err)
						require.Empty(t, progress.Message.GetOldestEvaluatingKeys())
						progress.Discard()
						return nil
					}),
				)
			})
		}
	})

	t.Run("Cycle", func(t *testing.T) {
		// Provide a computer that does nothing more than
		// request its own value recursively. This should
		// immediately trigger cycle detection.
		computer := NewMockComputerForTesting(ctrl)
		computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
				v := e.GetValue(model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata](key.Message), uint64ValueUnmarshaler)
				require.False(t, v.IsSet())
				return model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata]{}, model_evaluation.ErrMissingDependency
			})
		computer.EXPECT().IsLookup("type.googleapis.com/google.protobuf.UInt32Value").Return(false).AnyTimes()
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagStore := NewMockBoundStoreForTesting(ctrl)
		tagStore.EXPECT().ResolveTag(gomock.Any(), gomock.Any()).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
		evaluationReader := NewMockProtoEvaluationReaderForTesting(ctrl)
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
			testChunkingParameters,
			objectManager,
			tagStore,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("0e847672a7a34ba848ec92f4000a9f86049e5557496cfcede0db7744bf77c12b", 8575, 0, 0, 0),
			evaluationReader,
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
			/* cacheLookupResultIndirectionMaximumAge = */ 24*time.Hour,
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

		// The error should contain the cycle, starting and
		// ending with the key that depends on itself.
		err = program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
			return recursiveComputer.WaitForEvaluation(ctx, keyState)
		})
		require.Equal(t, []uint32{42, 42}, getCycleTestStackTrace(t, ctx, recursiveComputer, err))
	})

	t.Run("CycleBehindDependency", func(t *testing.T) {
		// The requested key does not take part in a cycle, but
		// depends on keys that do. Cycle detection should
		// cause the keys on the cycle to fail, as opposed to
		// picking the requested key and making no progress.
		computer := NewMockComputerForTesting(ctrl)
		computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
				// 100 -> 1 -> 2 -> 1.
				dependency := uint32(1)
				if key.Message.(*wrapperspb.UInt32Value).Value == 1 {
					dependency = 2
				}
				v := e.GetValue(model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](&wrapperspb.UInt32Value{
					Value: dependency,
				}), uint64ValueUnmarshaler)
				require.False(t, v.IsSet())
				return model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata]{}, model_evaluation.ErrMissingDependency
			}).
			AnyTimes()
		computer.EXPECT().IsLookup("type.googleapis.com/google.protobuf.UInt32Value").Return(false).AnyTimes()
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagStore := NewMockBoundStoreForTesting(ctrl)
		tagStore.EXPECT().ResolveTag(gomock.Any(), gomock.Any()).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
		lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
		lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()

		queuesFactory := model_evaluation.NewSimpleRecursiveComputerEvaluationQueuesFactory[object.LocalReference, model_core.ReferenceMetadata](1)
		queues := queuesFactory.NewQueues()
		recursiveComputer := model_evaluation.NewRecursiveComputer(
			computer,
			queues,
			object.SHA256V1ReferenceFormat,
			testChunkingParameters,
			objectManager,
			tagStore,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("0e847672a7a34ba848ec92f4000a9f86049e5557496cfcede0db7744bf77c12b", 8575, 0, 0, 0),
			NewMockProtoEvaluationReaderForTesting(ctrl),
			lookupResultReader,
			NewMockKeysReaderForTesting(ctrl),
			NewMockDeterministicBinaryEncoder(ctrl),
			NewMockKeyedBinaryEncoder(ctrl),
			clock.SystemClock,
			/* cacheLookupResultIndirectionMaximumAge = */ 24*time.Hour,
		)

		keyState, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 100,
						},
					),
				),
			),
		)
		require.NoError(t, err)

		timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		err = program.RunLocal(timeoutCtx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
			return recursiveComputer.WaitForEvaluation(ctx, keyState)
		})
		// The error should contain the cycle between keys 1
		// and 2, starting and ending with the key on the cycle
		// that the requested key depends on.
		require.Equal(t, []uint32{1, 2, 1}, getCycleTestStackTrace(t, ctx, recursiveComputer, err))
	})

	t.Run("CycleWithInterdependentBlockedKeys", func(t *testing.T) {
		// Key 1 is part of a cycle, and blocks keys 2 and 3.
		// Key 3 is also blocked on key 2. Failing key 2 should
		// not interfere with unblocking key 3.
		computer := NewMockComputerForTesting(ctrl)
		computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
				// 100 -> 2, 2 -> 1, 1 -> {1, 3}, 3 -> {1, 2}.
				dependencies := map[uint32][]uint32{
					100: {2},
					2:   {1},
					1:   {1, 3},
					3:   {1, 2},
				}[key.Message.(*wrapperspb.UInt32Value).Value]
				for _, dependency := range dependencies {
					v := e.GetValue(model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](&wrapperspb.UInt32Value{
						Value: dependency,
					}), uint64ValueUnmarshaler)
					require.False(t, v.IsSet())
				}
				return model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata]{}, model_evaluation.ErrMissingDependency
			}).
			AnyTimes()
		computer.EXPECT().IsLookup("type.googleapis.com/google.protobuf.UInt32Value").Return(false).AnyTimes()
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagStore := NewMockBoundStoreForTesting(ctrl)
		tagStore.EXPECT().ResolveTag(gomock.Any(), gomock.Any()).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
		lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
		lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()

		queuesFactory := model_evaluation.NewSimpleRecursiveComputerEvaluationQueuesFactory[object.LocalReference, model_core.ReferenceMetadata](1)
		queues := queuesFactory.NewQueues()
		recursiveComputer := model_evaluation.NewRecursiveComputer(
			computer,
			queues,
			object.SHA256V1ReferenceFormat,
			testChunkingParameters,
			objectManager,
			tagStore,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("0e847672a7a34ba848ec92f4000a9f86049e5557496cfcede0db7744bf77c12b", 8575, 0, 0, 0),
			NewMockProtoEvaluationReaderForTesting(ctrl),
			lookupResultReader,
			NewMockKeysReaderForTesting(ctrl),
			NewMockDeterministicBinaryEncoder(ctrl),
			NewMockKeyedBinaryEncoder(ctrl),
			clock.SystemClock,
			/* cacheLookupResultIndirectionMaximumAge = */ 24*time.Hour,
		)

		keyState, err := recursiveComputer.GetOrCreateKeyState(
			util.Must(
				model_core.MarshalTopLevelAny(
					model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 100,
						},
					),
				),
			),
		)
		require.NoError(t, err)

		timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		err = program.RunLocal(timeoutCtx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
			return recursiveComputer.WaitForEvaluation(ctx, keyState)
		})
		require.ErrorContains(t, err, "cyclic dependency detected")
	})

	t.Run("ErrorPropagation", func(t *testing.T) {
		// Have a simple function that calls a couple of levels
		// deep. At some point this will return an error, which
		// should be propagated all the way back up.
		computer := NewMockComputerForTesting(ctrl)
		computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
				k := key.Message.(*wrapperspb.UInt32Value)
				if k.Value == 0 {
					return model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata]{}, errors.New("reached zero")
				}

				v := e.GetValue(model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](&wrapperspb.UInt32Value{
					Value: k.Value - 1,
				}), uint64ValueUnmarshaler)
				if !v.IsSet() {
					return model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata]{}, model_evaluation.ErrMissingDependency
				}
				return protoValue(
					&emptypb.Empty{},
				), nil
			}).
			Times(6)
		computer.EXPECT().IsLookup("type.googleapis.com/google.protobuf.UInt32Value").Return(false).AnyTimes()
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagStore := NewMockBoundStoreForTesting(ctrl)
		tagStore.EXPECT().ResolveTag(gomock.Any(), gomock.Any()).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
		evaluationReader := NewMockProtoEvaluationReaderForTesting(ctrl)
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
			testChunkingParameters,
			objectManager,
			tagStore,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("e5283197708f96f2368701a89fcdd72367106497f0335bd2d5f3403a826d71da", 8584, 0, 0, 0),
			evaluationReader,
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
			/* cacheLookupResultIndirectionMaximumAge = */ 24*time.Hour,
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
		computer.EXPECT().IsLookup("type.googleapis.com/google.protobuf.UInt32Value").Return(false).AnyTimes()
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagStore := NewMockBoundStoreForTesting(ctrl)
		evaluationReader := NewMockProtoEvaluationReaderForTesting(ctrl)
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
			testChunkingParameters,
			objectManager,
			tagStore,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("10479a81dafa74a2f72438cbce7d472cb9e8ea3648a9a2ec3622a27620a02925", 23721, 0, 0, 0),
			evaluationReader,
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
			/* cacheLookupResultIndirectionMaximumAge = */ 24*time.Hour,
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

		tagStore.EXPECT().ResolveTag(
			gomock.Any(),
			[...]byte{
				0xbf, 0xba, 0xc9, 0x92, 0x7b, 0xff, 0x1f, 0xfe,
				0xd1, 0x95, 0x69, 0xab, 0x3a, 0x78, 0x6c, 0xf7,
				0x56, 0x95, 0x13, 0x19, 0x5f, 0xbe, 0x80, 0xc5,
				0x74, 0x76, 0x7c, 0x78, 0xea, 0x47, 0x94, 0xc6,
			},
		).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist"))
		computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
				return protoValue(
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
		computer.EXPECT().IsLookup("type.googleapis.com/google.protobuf.UInt32Value").Return(false).AnyTimes()
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagStore := NewMockBoundStoreForTesting(ctrl)
		evaluationReader := NewMockProtoEvaluationReaderForTesting(ctrl)
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
			testChunkingParameters,
			objectManager,
			tagStore,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("0c1c0eacebd721476e1a158da2e3ee0281c9c6fe9e8f9e2941f2a05153869b32", 48374, 0, 0, 0),
			evaluationReader,
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
			/* cacheLookupResultIndirectionMaximumAge = */ 24*time.Hour,
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

		tagStore.EXPECT().ResolveTag(
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
		computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
				return protoValue(
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
		computer.EXPECT().IsLookup("type.googleapis.com/google.protobuf.UInt32Value").Return(false).AnyTimes()
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagStore := NewMockBoundStoreForTesting(ctrl)
		evaluationReader := NewMockProtoEvaluationReaderForTesting(ctrl)
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
			testChunkingParameters,
			objectManager,
			tagStore,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("4466f601ba900ce4e0d606dc68dd0c35c5e976792784400514f42905d6deba6e", 64722, 0, 0, 0),
			evaluationReader,
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
			/* cacheLookupResultIndirectionMaximumAge = */ 24*time.Hour,
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

		tagStore.EXPECT().ResolveTag(
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
				Result: &model_evaluation_cache_pb.LookupResult_MissingDependencies_{MissingDependencies: &model_evaluation_cache_pb.LookupResult_MissingDependencies{}},
			},
		), nil)
		computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
				return protoValue(
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
		computer.EXPECT().IsLookup("type.googleapis.com/google.protobuf.UInt32Value").Return(false).AnyTimes()
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagStore := NewMockBoundStoreForTesting(ctrl)
		evaluationReader := NewMockProtoEvaluationReaderForTesting(ctrl)
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
			testChunkingParameters,
			objectManager,
			tagStore,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("2fbd13f1ee48876ec1e85961b59877c3916fb10ae23e7e1e45083feecafe1804", 57483, 0, 0, 0),
			evaluationReader,
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
			/* cacheLookupResultIndirectionMaximumAge = */ 24*time.Hour,
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

		tagStore.EXPECT().ResolveTag(
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
				Result: &model_evaluation_cache_pb.LookupResult_HitValue{HitValue: model_core_pb.Opaque_builder{
					Value: util.Must(proto.Marshal(&wrapperspb.UInt64Value{
						Value: 26,
					})),
					References: &model_core_pb.ReferenceSet{},
				}.Build()},
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

	t.Run("CacheLookupUnsortedKeys", func(t *testing.T) {
		// Lists of keys stored in cache entries are sorted by
		// key reference. Lists that are not sorted must be
		// treated as malformed. This should cause the key to be
		// evaluated, without creating KeyStates for any of the
		// keys contained in the list.
		computer := NewMockComputerForTesting(ctrl)
		computer.EXPECT().IsLookup("type.googleapis.com/google.protobuf.UInt32Value").Return(false).AnyTimes()
		objectManager := NewMockObjectManagerForTesting(ctrl)
		tagStore := NewMockBoundStoreForTesting(ctrl)
		evaluationReader := NewMockProtoEvaluationReaderForTesting(ctrl)
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
			testChunkingParameters,
			objectManager,
			tagStore,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("bf1b2cdd5f58461827eb9285ee37dd45fea02ebc4e278f28678ea2722f590337", 57483, 0, 0, 0),
			evaluationReader,
			lookupResultReader,
			keysReader,
			cacheDeterministicEncoder,
			cacheKeyedEncoder,
			clock.SystemClock,
			/* cacheLookupResultIndirectionMaximumAge = */ 24*time.Hour,
		)

		keyState, err := recursiveComputer.GetOrCreateKeyState(cacheTestKey(18))
		require.NoError(t, err)

		// Initial lookup for &wrapperspb.UInt32Value{Value: 18},
		// returning lists of keys in descending order.
		newKeysLeaf := func(n uint32) *model_evaluation_pb.Keys {
			return &model_evaluation_pb.Keys{
				Level: &model_evaluation_pb.Keys_Leaf{Leaf: model_core_pb.Any_builder{
					Value:      util.Must(anypb.New(&wrapperspb.UInt32Value{Value: n})),
					References: &model_core_pb.ReferenceSet{},
				}.Build()},
			}
		}
		low, high := uint32(16), uint32(17)
		if bytes.Compare(cacheTestKeyReference(low).GetRawReference(), cacheTestKeyReference(high).GetRawReference()) > 0 {
			low, high = high, low
		}
		tagStore.EXPECT().ResolveTag(
			gomock.Any(),
			[...]byte{
				0xba, 0x4e, 0xe4, 0x47, 0x51, 0x09, 0x77, 0x2d,
				0x0f, 0x8f, 0x90, 0x9d, 0xce, 0xc6, 0xe2, 0x14,
				0x6a, 0xee, 0xed, 0x9c, 0x1d, 0x44, 0x9d, 0x3f,
				0xd7, 0x49, 0x74, 0xf1, 0x93, 0xad, 0xb0, 0x52,
			},
		).Return(object.MustNewSHA256V1LocalReference("cd897b5bbc720746ba0740100714e8f98f36e3c3cda42b3d3e8c0c26884c0780", 43984, 0, 0, 0), nil)
		lookupResultReader.EXPECT().ReadObject(gomock.Any(), gomock.Any()).Return(model_core.NewSimpleMessage[object.LocalReference](
			&model_evaluation_cache_pb.LookupResult{
				Result: &model_evaluation_cache_pb.LookupResult_Initial_{Initial: &model_evaluation_cache_pb.LookupResult_Initial{
					GraphletVariableDependencyKeys: []*model_evaluation_pb.Keys{newKeysLeaf(high), newKeysLeaf(low)},
					ValueVariableDependencyKeys:    []*model_evaluation_pb.Keys{newKeysLeaf(high), newKeysLeaf(low)},
				}},
			},
		), nil)

		computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
				testutil.RequireEqualProto(t, &wrapperspb.UInt32Value{
					Value: 18,
				}, key.Message)
				return protoValue(
					&wrapperspb.UInt32Value{
						Value: 42,
					},
				), nil
			})

		require.NoError(
			t,
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
				return recursiveComputer.WaitForEvaluation(ctx, keyState)
			}),
		)
	})
}

func TestRecursiveComputerObject(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	sourceKey := func(value uint32) model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata] {
		return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](&wrapperspb.UInt32Value{Value: value})
	}
	marshalKey := func(value uint32) model_core.TopLevelMessage[*anypb.Any, object.LocalReference] {
		return util.Must(
			model_core.MarshalTopLevelAny(
				model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
					&wrapperspb.UInt32Value{Value: value},
				),
			),
		)
	}
	newRecursiveComputer := func(computer *MockComputerForTesting) (*model_evaluation.RecursiveComputer[object.LocalReference, model_core.ReferenceMetadata], model_evaluation.RecursiveComputerEvaluationQueues[object.LocalReference, model_core.ReferenceMetadata]) {
		computer.EXPECT().IsLookup("type.googleapis.com/google.protobuf.UInt32Value").Return(false).AnyTimes()
		tagStore := NewMockBoundStoreForTesting(ctrl)
		tagStore.EXPECT().ResolveTag(gomock.Any(), gomock.Any()).Return(object.LocalReference{}, status.Error(codes.NotFound, "Tag does not exist")).AnyTimes()
		lookupResultReader := NewMockLookupResultReaderForTesting(ctrl)
		lookupResultReader.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
		queues := model_evaluation.NewSimpleRecursiveComputerEvaluationQueuesFactory[object.LocalReference, model_core.ReferenceMetadata](1).NewQueues()
		return model_evaluation.NewRecursiveComputer(
			computer,
			queues,
			object.SHA256V1ReferenceFormat,
			testChunkingParameters,
			NewMockObjectManagerForTesting(ctrl),
			tagStore,
			/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("f07997aa26d63ad33c8b2e6f920ae9b42c93bacb67c84ae529d065c6d572d342", 2323, 0, 0, 0),
			NewMockProtoEvaluationReaderForTesting(ctrl),
			lookupResultReader,
			NewMockKeysReaderForTesting(ctrl),
			model_encoding.NewChainedDeterministicBinaryEncoder(nil),
			NewMockKeyedBinaryEncoder(ctrl),
			clock.SystemClock,
			/* cacheLookupResultIndirectionMaximumAge = */ 24*time.Hour,
		), queues
	}
	computeValue := func(onObject func(any)) func(context.Context, model_core.Message[proto.Message, object.LocalReference], model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
		return func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
			// Key 0 is the source of the object. Key
			// 3 depends on keys 1 and 2, which both request
			// the object.
			switch k := key.Message.(*wrapperspb.UInt32Value).Value; k {
			case 0:
				return protoValue(&wrapperspb.UInt32Value{Value: 1000}), nil
			case 3:
				v1 := e.GetValue(sourceKey(1), uint32ValueUnmarshaler)
				v2 := e.GetValue(sourceKey(2), uint32ValueUnmarshaler)
				if !v1.IsSet() || !v2.IsSet() {
					return model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata]{}, model_evaluation.ErrMissingDependency
				}
				return protoValue(&wrapperspb.UInt32Value{Value: 3}), nil
			default:
				object, ok := e.GetObject(sourceKey(0))
				if !ok {
					return model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata]{}, model_evaluation.ErrMissingDependency
				}
				onObject(object)
				return protoValue(&wrapperspb.UInt32Value{Value: k}), nil
			}
		}
	}

	t.Run("Success", func(t *testing.T) {
		// The conversion should only be performed once, and be
		// given the value of the key. All consumers should see
		// the same instance.
		var objects []any
		computer := NewMockComputerForTesting(ctrl)
		computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(computeValue(func(object any) {
				objects = append(objects, object)
			})).
			AnyTimes()
		instance := new(int)
		computer.EXPECT().ComputeObject("type.googleapis.com/google.protobuf.UInt32Value", gomock.Any()).
			DoAndReturn(func(keyTypeURL string, value model_core.TopLevelMessage[[]byte, object.LocalReference]) (any, error) {
				v, err := model_format.NewProto[wrapperspb.UInt32Value]().Unmarshal(value.Message)
				require.NoError(t, err)
				require.Equal(t, uint32(1000), v.Value)
				return instance, nil
			})
		recursiveComputer, queues := newRecursiveComputer(computer)

		keyState, err := recursiveComputer.GetOrCreateKeyState(marshalKey(3))
		require.NoError(t, err)
		require.NoError(t, program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
			return recursiveComputer.WaitForEvaluation(ctx, keyState)
		}))
		require.Len(t, objects, 2)
		require.Same(t, instance, objects[0])
		require.Same(t, instance, objects[1])
	})

	t.Run("ConversionFailure", func(t *testing.T) {
		// Failures to perform the conversion should be reported
		// against the key whose value is being converted.
		computer := NewMockComputerForTesting(ctrl)
		computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(computeValue(func(any) {
				t.Error("Object should not have been provided")
			})).
			AnyTimes()
		computer.EXPECT().ComputeObject("type.googleapis.com/google.protobuf.UInt32Value", gomock.Any()).Return(nil, errors.New("invalid value"))
		recursiveComputer, queues := newRecursiveComputer(computer)

		keyState, err := recursiveComputer.GetOrCreateKeyState(marshalKey(1))
		require.NoError(t, err)
		sourceKeyState, err := recursiveComputer.GetOrCreateKeyState(marshalKey(0))
		require.NoError(t, err)
		require.Equal(
			t,
			model_evaluation.NestedError[object.LocalReference, model_core.ReferenceMetadata]{
				KeyState: sourceKeyState,
				Err:      errors.New("invalid value"),
			},
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
				return recursiveComputer.WaitForEvaluation(ctx, keyState)
			}),
		)
	})

	t.Run("UnmarshalFailure", func(t *testing.T) {
		// Failures to unmarshal the value of a dependency
		// should be reported against the dependency.
		computer := NewMockComputerForTesting(ctrl)
		computer.EXPECT().ComputeValue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata], error) {
				if key.Message.(*wrapperspb.UInt32Value).Value == 0 {
					return protoValue(&wrapperspb.UInt32Value{Value: 1000}), nil
				}
				if v := e.GetValue(sourceKey(0), failingUnmarshaler{}); v.IsSet() {
					t.Error("Value should not have been provided")
				}
				return model_core.PatchedMessage[encoding.BinaryMarshaler, model_core.ReferenceMetadata]{}, model_evaluation.ErrMissingDependency
			}).
			AnyTimes()
		recursiveComputer, queues := newRecursiveComputer(computer)

		keyState, err := recursiveComputer.GetOrCreateKeyState(marshalKey(1))
		require.NoError(t, err)
		sourceKeyState, err := recursiveComputer.GetOrCreateKeyState(marshalKey(0))
		require.NoError(t, err)
		require.Equal(
			t,
			model_evaluation.NestedError[object.LocalReference, model_core.ReferenceMetadata]{
				KeyState: sourceKeyState,
				Err:      errors.New("corrupt value"),
			},
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
				return recursiveComputer.WaitForEvaluation(ctx, keyState)
			}),
		)
	})
}

// failingUnmarshaler is an implementation of model_format.Unmarshaler
// that always fails.
type failingUnmarshaler struct{}

func (failingUnmarshaler) Unmarshal(data []byte) (any, error) {
	return nil, errors.New("corrupt value")
}

// getCycleTestStackTrace returns the keys contained in a chain of
// NestedErrors, and checks that the chain ends with an error
// indicating that a cyclic dependency was detected.
func getCycleTestStackTrace(t *testing.T, ctx context.Context, recursiveComputer *model_evaluation.RecursiveComputer[object.LocalReference, model_core.ReferenceMetadata], err error) []uint32 {
	var keys []uint32
	for {
		var nestedErr model_evaluation.NestedError[object.LocalReference, model_core.ReferenceMetadata]
		if !errors.As(err, &nestedErr) {
			break
		}
		key, errKey := recursiveComputer.GetKeyStateKeyMessage(ctx, nestedErr.KeyState)
		require.NoError(t, errKey)
		value, errKey := model_core.UnmarshalTopLevelAnyNew(key)
		require.NoError(t, errKey)
		keys = append(keys, value.Message.(*wrapperspb.UInt32Value).Value)
		err = nestedErr.Err
	}
	require.EqualError(t, err, "cyclic dependency detected")
	return keys
}
