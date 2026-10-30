package analysis_test

import (
	"encoding"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

// newTestSuiteTests creates a B-tree containing the labels of tests
// that are part of a test suite, consisting only of leaves.
func newTestSuiteTests(labels ...string) []*model_starlark_pb.TestSuite_Test {
	tests := make([]*model_starlark_pb.TestSuite_Test, 0, len(labels))
	for _, l := range labels {
		tests = append(tests, &model_starlark_pb.TestSuite_Test{
			Level: &model_starlark_pb.TestSuite_Test_Leaf{Leaf: l},
		})
	}
	return tests
}

func TestTestSuiteTests(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	allSizes := []model_starlark_pb.TestSize{
		model_starlark_pb.TestSize_SMALL,
		model_starlark_pb.TestSize_MEDIUM,
		model_starlark_pb.TestSize_LARGE,
		model_starlark_pb.TestSize_ENORMOUS,
	}
	testSuiteDefinition := func(testSuite *model_starlark_pb.TestSuite) *model_starlark_pb.Target_Definition {
		return &model_starlark_pb.Target_Definition{
			Kind: &model_starlark_pb.Target_Definition_TestSuite{
				TestSuite: testSuite,
			},
		}
	}
	ruleTargetDefinition := func(test *model_starlark_pb.RuleTarget_Test, tags ...string) *model_starlark_pb.Target_Definition {
		return &model_starlark_pb.Target_Definition{
			Kind: &model_starlark_pb.Target_Definition_RuleTarget{
				RuleTarget: &model_starlark_pb.RuleTarget{
					RuleIdentifier: "@@rules+//:defs.bzl%my_rule",
					Tags:           tags,
					Test:           test,
				},
			},
		}
	}
	testOfSize := func(size model_starlark_pb.TestSize) *model_starlark_pb.RuleTarget_Test {
		return &model_starlark_pb.RuleTarget_Test{Size: size}
	}
	expectTarget := func(e *MockTestSuiteTestsEnvironmentForTesting, l string, definition *model_starlark_pb.Target_Definition) {
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: l}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Target_Value{
			Definition: definition,
		}))
	}
	computeTestSuiteTests := func(e *MockTestSuiteTestsEnvironmentForTesting, l string) ([]string, error) {
		value, err := bct.computer.ComputeTestSuiteTestsValue(ctx, &model_analysis_pb.TestSuiteTests_Key{Label: l}, e)
		if err != nil {
			return nil, err
		}
		return getTargetLabels(t, value.Message.TargetLabels), nil
	}

	t.Run("NotATestSuite", func(t *testing.T) {
		e := NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		expectTarget(e, "@@main+//foo:t", ruleTargetDefinition(testOfSize(model_starlark_pb.TestSize_SMALL)))

		_, err := computeTestSuiteTests(e, "@@main+//foo:t")
		require.EqualError(t, err, "target \"@@main+//foo:t\" is not a test suite")
	})

	t.Run("NoTests", func(t *testing.T) {
		// Test suites that don't list any tests explicitly have
		// the tests in their package filled in when the package
		// is loaded. If no tests are present, the test suite is
		// empty. The package should not be inspected again.
		e := NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		expectTarget(e, "@@main+//foo:suite", testSuiteDefinition(&model_starlark_pb.TestSuite{Sizes: allSizes}))

		labels, err := computeTestSuiteTests(e, "@@main+//foo:suite")
		require.NoError(t, err)
		require.Empty(t, labels)
	})

	t.Run("ExplicitTests", func(t *testing.T) {
		// Tests listed explicitly are subject to the test
		// suite's filters, even if tagged "manual". Tests that
		// are part of nested test suites are included without
		// applying these filters.
		e := NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		expectTarget(e, "@@main+//foo:suite", testSuiteDefinition(&model_starlark_pb.TestSuite{
			Tests: newTestSuiteTests(
				"@@main+//bar:nested",
				"@@main+//foo:manual",
				"@@main+//foo:unrelated",
			),
			PositiveTags: []string{"integration"},
			Sizes:        allSizes,
		}))
		expectTarget(e, "@@main+//bar:nested", testSuiteDefinition(&model_starlark_pb.TestSuite{
			Tests: newTestSuiteTests("@@main+//bar:a"),
			Sizes: allSizes,
		}))
		e.EXPECT().GetTestSuiteTestsValue(
			testutil.EqProto(t, &model_analysis_pb.TestSuiteTests_Key{Label: "@@main+//bar:nested"}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.TestSuiteTests_Value{
			TargetLabels: newTargetLabels("@@main+//bar:a", "@@main+//qux:z"),
		}))
		expectTarget(e, "@@main+//foo:manual", ruleTargetDefinition(testOfSize(model_starlark_pb.TestSize_SMALL), "integration", "manual"))
		expectTarget(e, "@@main+//foo:unrelated", ruleTargetDefinition(testOfSize(model_starlark_pb.TestSize_SMALL)))

		labels, err := computeTestSuiteTests(e, "@@main+//foo:suite")
		require.NoError(t, err)
		require.Equal(t, []string{
			"@@main+//bar:a",
			"@@main+//foo:manual",
			"@@main+//qux:z",
		}, labels)
	})

	t.Run("TestsInOtherObjects", func(t *testing.T) {
		// Lists of tests that are too large to store in a
		// single object are stored as B-trees.
		e := NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: "@@main+//foo:suite"}),
		).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.Target_Value {
			return &model_analysis_pb.Target_Value{
				Definition: testSuiteDefinition(&model_starlark_pb.TestSuite{
					Tests: []*model_starlark_pb.TestSuite_Test{
						{
							Level: &model_starlark_pb.TestSuite_Test_Parent_{
								Parent: &model_starlark_pb.TestSuite_Test_Parent{
									Reference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
										return model_core.NewProtoListBinaryMarshaler(newTestSuiteTests("@@main+//foo:a", "@@main+//foo:b"))
									})),
								},
							},
						},
						{
							Level: &model_starlark_pb.TestSuite_Test_Leaf{Leaf: "@@main+//foo:c"},
						},
					},
					Sizes: allSizes,
				}),
			}
		}))
		expectTarget(e, "@@main+//foo:a", ruleTargetDefinition(testOfSize(model_starlark_pb.TestSize_SMALL)))
		expectTarget(e, "@@main+//foo:b", ruleTargetDefinition(testOfSize(model_starlark_pb.TestSize_SMALL)))
		expectTarget(e, "@@main+//foo:c", ruleTargetDefinition(testOfSize(model_starlark_pb.TestSize_SMALL)))

		labels, err := computeTestSuiteTests(e, "@@main+//foo:suite")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//foo:a", "@@main+//foo:b", "@@main+//foo:c"}, labels)
	})

	t.Run("UnresolvableLabel", func(t *testing.T) {
		// Test suites may refer to tests in repos that are not
		// visible (e.g., dev dependencies of modules that are
		// not the root module). This should only cause an error
		// when the test suite is expanded.
		e := NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		expectTarget(e, "@@main+//foo:suite", testSuiteDefinition(&model_starlark_pb.TestSuite{
			Tests: newTestSuiteTests("@@[unknown repo 'dev' requested from main+]//:t"),
			Sizes: allSizes,
		}))

		_, err := computeTestSuiteTests(e, "@@main+//foo:suite")
		require.ErrorContains(t, err, "unknown repo 'dev' requested from main+")
	})

	t.Run("MissingDependencies", func(t *testing.T) {
		// All targets should be requested before returning
		// ErrMissingDependency, so that they can be loaded in
		// parallel.
		e := NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		expectTarget(e, "@@main+//foo:suite", testSuiteDefinition(&model_starlark_pb.TestSuite{
			Tests: newTestSuiteTests("@@main+//foo:a", "@@main+//foo:b", "@@main+//foo:nested"),
			Sizes: allSizes,
		}))
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: "@@main+//foo:a"}),
		).Return(model_core.Message[*model_analysis_pb.Target_Value, model_core.CreatedObjectTree]{})
		expectTarget(e, "@@main+//foo:b", ruleTargetDefinition(testOfSize(model_starlark_pb.TestSize_SMALL)))
		expectTarget(e, "@@main+//foo:nested", testSuiteDefinition(&model_starlark_pb.TestSuite{Sizes: allSizes}))
		e.EXPECT().GetTestSuiteTestsValue(
			testutil.EqProto(t, &model_analysis_pb.TestSuiteTests_Key{Label: "@@main+//foo:nested"}),
		).Return(model_core.Message[*model_analysis_pb.TestSuiteTests_Value, model_core.CreatedObjectTree]{})

		_, err := computeTestSuiteTests(e, "@@main+//foo:suite")
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})

	t.Run("NeitherTestNorTestSuite", func(t *testing.T) {
		e := NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		expectTarget(e, "@@main+//foo:suite", testSuiteDefinition(&model_starlark_pb.TestSuite{
			Tests: newTestSuiteTests("@@main+//foo:lib"),
			Sizes: allSizes,
		}))
		expectTarget(e, "@@main+//foo:lib", ruleTargetDefinition(nil))

		_, err := computeTestSuiteTests(e, "@@main+//foo:suite")
		require.EqualError(t, err, "target \"@@main+//foo:lib\" is neither a test nor a test suite")
	})
}
