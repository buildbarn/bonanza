package evaluation_test

import (
	"context"
	"errors"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

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
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata], error) {
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
					return model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata]{}, evaluation.ErrMissingDependency
				}
				return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](
					&wrapperspb.UInt64Value{
						Value: v0.Message.(*wrapperspb.UInt64Value).Value + v1.Message.(*wrapperspb.UInt64Value).Value,
					},
				), nil
			}).
			AnyTimes()
		objectManager := NewMockObjectManagerForTesting(ctrl)

		recursiveComputer := evaluation.NewRecursiveComputer(computer, objectManager)
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
				dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
					for recursiveComputer.ProcessNextQueuedKey(ctx) {
					}
					return nil
				})

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
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata], error) {
				v := e.GetMessageValue(model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata](key.Message))
				require.False(t, v.IsSet())
				return model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata]{}, evaluation.ErrMissingDependency
			})
		objectManager := NewMockObjectManagerForTesting(ctrl)

		recursiveComputer := evaluation.NewRecursiveComputer(computer, objectManager)
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
			evaluation.NestedError[object.LocalReference]{
				Key: model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
					&wrapperspb.UInt32Value{
						Value: 42,
					},
				),
				Err: errors.New("cyclic evaluation detected"),
			},
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
					for recursiveComputer.ProcessNextQueuedKey(ctx) {
					}
					return nil
				})

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
			DoAndReturn(func(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata], error) {
				k := key.Message.(*wrapperspb.UInt32Value)
				if k.Value == 0 {
					return model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata]{}, errors.New("reached zero")
				}

				v := e.GetMessageValue(model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](&wrapperspb.UInt32Value{
					Value: k.Value - 1,
				}))
				if !v.IsSet() {
					return model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata]{}, evaluation.ErrMissingDependency
				}
				return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](
					&emptypb.Empty{},
				), nil
			}).
			Times(4)
		objectManager := NewMockObjectManagerForTesting(ctrl)

		recursiveComputer := evaluation.NewRecursiveComputer(computer, objectManager)

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
			evaluation.NestedError[object.LocalReference]{
				Key: model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
					&wrapperspb.UInt32Value{
						Value: 1,
					},
				),
				Err: evaluation.NestedError[object.LocalReference]{
					Key: model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 0,
						},
					),
					Err: errors.New("reached zero"),
				},
			},
			program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
					for recursiveComputer.ProcessNextQueuedKey(ctx) {
					}
					return nil
				})

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
			evaluation.NestedError[object.LocalReference]{
				Key: model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
					&wrapperspb.UInt32Value{
						Value: 2,
					},
				),
				Err: evaluation.NestedError[object.LocalReference]{
					Key: model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
						&wrapperspb.UInt32Value{
							Value: 1,
						},
					),
					Err: evaluation.NestedError[object.LocalReference]{
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
				dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
					for recursiveComputer.ProcessNextQueuedKey(ctx) {
					}
					return nil
				})

				_, err := recursiveComputer.WaitForMessageValue(ctx, keyState3)
				return err
			}),
		)
	})
}
