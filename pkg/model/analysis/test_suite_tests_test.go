package analysis_test

import (
	"encoding"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_format "bonanza.build/pkg/model/format"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"

	"go.uber.org/mock/gomock"
)

// newTestSuiteTests creates a B-tree containing the labels of tests
// that are part of a test suite, consisting only of leaves.
func newTestSuiteTests(labels ...string) []*model_starlark_pb.TestSuite_Test {
	tests := make([]*model_starlark_pb.TestSuite_Test, 0, len(labels))
	for _, l := range labels {
		tests = append(tests, model_starlark_pb.TestSuite_Test_builder{
			Leaf: proto.String(l),
		}.Build())
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
		return model_starlark_pb.Target_Definition_builder{
			TestSuite: proto.ValueOrDefault(testSuite),
		}.Build()
	}
	ruleTargetDefinition := func(test *model_starlark_pb.RuleTarget_Test, tags ...string) *model_starlark_pb.Target_Definition {
		return model_starlark_pb.Target_Definition_builder{
			RuleTarget: model_starlark_pb.RuleTarget_builder{
				RuleIdentifier: "@@rules+//:defs.bzl%my_rule",
				Tags:           tags,
				Test:           test,
			}.Build(),
		}.Build()
	}
	testOfSize := func(size model_starlark_pb.TestSize) *model_starlark_pb.RuleTarget_Test {
		return model_starlark_pb.RuleTarget_Test_builder{Size: size}.Build()
	}
	expectTarget := func(e *MockTestSuiteTestsEnvironmentForTesting, l string, definition *model_starlark_pb.Target_Definition) {
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: l}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
			Definition: definition,
		}.Build()))
	}
	computeTestSuiteTests := func(e *MockTestSuiteTestsEnvironmentForTesting, l string) ([]string, error) {
		value, err := bct.computer.ComputeTestSuiteTestsValue(ctx, model_analysis_pb.TestSuiteTests_Key_builder{Label: l}.Build(), e)
		if err != nil {
			return nil, err
		}
		return getTargetLabels(t, value.Message.GetTargetLabels()), nil
	}

	t.Run("NotATestSuite", func(t *testing.T) {
		e := NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		expectTarget(e, "@@main+//foo:t", ruleTargetDefinition(testOfSize(model_starlark_pb.TestSize_SMALL)))

		_, err := computeTestSuiteTests(e, "@@main+//foo:t")
		require.EqualError(t, err, "target \"@@main+//foo:t\" is not a test suite")
	})

	t.Run("DoesNotExist", func(t *testing.T) {
		// Both the test suite and the tests it contains need
		// to exist.
		e := NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		expectTarget(e, "@@main+//foo:suite", nil)

		_, err := computeTestSuiteTests(e, "@@main+//foo:suite")
		require.EqualError(t, err, "target \"@@main+//foo:suite\" does not exist")

		e = NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		expectTarget(e, "@@main+//foo:suite", testSuiteDefinition(model_starlark_pb.TestSuite_builder{
			Tests: newTestSuiteTests("@@main+//foo:missing"),
			Sizes: allSizes,
		}.Build()))
		expectTarget(e, "@@main+//foo:missing", nil)

		_, err = computeTestSuiteTests(e, "@@main+//foo:suite")
		require.EqualError(t, err, "test \"@@main+//foo:missing\" does not exist")
	})

	t.Run("NoTests", func(t *testing.T) {
		// Test suites that don't list any tests explicitly have
		// the tests in their package filled in when the package
		// is loaded. If no tests are present, the test suite is
		// empty. The package should not be inspected again.
		e := NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		expectTarget(e, "@@main+//foo:suite", testSuiteDefinition(model_starlark_pb.TestSuite_builder{Sizes: allSizes}.Build()))

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
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		expectTarget(e, "@@main+//foo:suite", testSuiteDefinition(model_starlark_pb.TestSuite_builder{
			Tests: newTestSuiteTests(
				"@@main+//bar:nested",
				"@@main+//foo:manual",
				"@@main+//foo:unrelated",
			),
			PositiveTags: []string{"integration"},
			Sizes:        allSizes,
		}.Build()))
		expectTarget(e, "@@main+//bar:nested", testSuiteDefinition(model_starlark_pb.TestSuite_builder{
			Tests: newTestSuiteTests("@@main+//bar:a"),
			Sizes: allSizes,
		}.Build()))
		e.EXPECT().GetTestSuiteTestsValue(
			testutil.EqProto(t, model_analysis_pb.TestSuiteTests_Key_builder{Label: "@@main+//bar:nested"}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.TestSuiteTests_Value_builder{
			TargetLabels: newTargetLabels("@@main+//bar:a", "@@main+//qux:z"),
		}.Build()))
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
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//foo:suite"}.Build()),
		).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.Target_Value {
			return model_analysis_pb.Target_Value_builder{
				Definition: testSuiteDefinition(model_starlark_pb.TestSuite_builder{
					Tests: []*model_starlark_pb.TestSuite_Test{
						model_starlark_pb.TestSuite_Test_builder{
							Parent: model_starlark_pb.TestSuite_Test_Parent_builder{
								Reference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
									return model_format.NewProtoList[model_starlark_pb.TestSuite_Test]().NewMarshaler(newTestSuiteTests("@@main+//foo:a", "@@main+//foo:b"))
								})),
							}.Build(),
						}.Build(),
						model_starlark_pb.TestSuite_Test_builder{
							Leaf: proto.String("@@main+//foo:c"),
						}.Build(),
					},
					Sizes: allSizes,
				}.Build()),
			}.Build()
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
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		expectTarget(e, "@@main+//foo:suite", testSuiteDefinition(model_starlark_pb.TestSuite_builder{
			Tests: newTestSuiteTests("@@[unknown repo 'dev' requested from main+]//:t"),
			Sizes: allSizes,
		}.Build()))

		_, err := computeTestSuiteTests(e, "@@main+//foo:suite")
		require.ErrorContains(t, err, "unknown repo 'dev' requested from main+")
	})

	t.Run("MissingDependencies", func(t *testing.T) {
		// All targets should be requested before returning
		// ErrMissingDependency, so that they can be loaded in
		// parallel.
		e := NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		expectTarget(e, "@@main+//foo:suite", testSuiteDefinition(model_starlark_pb.TestSuite_builder{
			Tests: newTestSuiteTests("@@main+//foo:a", "@@main+//foo:b", "@@main+//foo:nested"),
			Sizes: allSizes,
		}.Build()))
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//foo:a"}.Build()),
		).Return(model_core.Message[*model_analysis_pb.Target_Value, model_core.CreatedObjectTree]{})
		expectTarget(e, "@@main+//foo:b", ruleTargetDefinition(testOfSize(model_starlark_pb.TestSize_SMALL)))
		expectTarget(e, "@@main+//foo:nested", testSuiteDefinition(model_starlark_pb.TestSuite_builder{Sizes: allSizes}.Build()))
		e.EXPECT().GetTestSuiteTestsValue(
			testutil.EqProto(t, model_analysis_pb.TestSuiteTests_Key_builder{Label: "@@main+//foo:nested"}.Build()),
		).Return(model_core.Message[*model_analysis_pb.TestSuiteTests_Value, model_core.CreatedObjectTree]{})

		_, err := computeTestSuiteTests(e, "@@main+//foo:suite")
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})

	t.Run("NeitherTestNorTestSuite", func(t *testing.T) {
		e := NewMockTestSuiteTestsEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		expectTarget(e, "@@main+//foo:suite", testSuiteDefinition(model_starlark_pb.TestSuite_builder{
			Tests: newTestSuiteTests("@@main+//foo:lib"),
			Sizes: allSizes,
		}.Build()))
		expectTarget(e, "@@main+//foo:lib", ruleTargetDefinition(nil))

		_, err := computeTestSuiteTests(e, "@@main+//foo:suite")
		require.EqualError(t, err, "target \"@@main+//foo:lib\" is neither a test nor a test suite")
	})
}
