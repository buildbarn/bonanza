package analysis

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

// allTestSuiteTests returns the labels of the tests and test suites
// that are listed in the "tests" attribute of a test suite. As this
// list may be large, it is stored as a B-tree.
func (c *baseComputer[TReference, TMetadata]) allTestSuiteTests(ctx context.Context, tests model_core.Message[[]*model_starlark_pb.TestSuite_Test, TReference], errOut *error) iter.Seq[string] {
	return func(yield func(string) bool) {
		for entry := range btree.AllLeaves(
			ctx,
			c.testSuiteTestReader,
			tests,
			func(entry model_core.Message[*model_starlark_pb.TestSuite_Test, TReference]) (*model_core_pb.DecodableReference, error) {
				return entry.Message.GetParent().GetReference(), nil
			},
			errOut,
		) {
			level, ok := entry.Message.Level.(*model_starlark_pb.TestSuite_Test_Leaf)
			if !ok {
				*errOut = errors.New("not a valid leaf entry")
				return
			}
			if !yield(level.Leaf) {
				return
			}
		}
	}
}

// newTargetLabelLeaves converts a set of labels to a list of
// TargetLabel messages that only contains leaves. This list can be
// merged with B-trees using mergeTargetLabels().
func newTargetLabelLeaves[TReference any](labels labelSet) model_core.Message[[]*model_analysis_pb.TargetLabel, TReference] {
	leaves := make([]*model_analysis_pb.TargetLabel, 0, len(labels))
	for _, l := range labels.sorted() {
		leaves = append(leaves, &model_analysis_pb.TargetLabel{
			Level: &model_analysis_pb.TargetLabel_Leaf{
				Leaf: l.String(),
			},
		})
	}
	return model_core.NewSimpleMessage[TReference](leaves)
}

func (c *baseComputer[TReference, TMetadata]) ComputeTestSuiteTestsValue(ctx context.Context, key *model_analysis_pb.TestSuiteTests_Key, e TestSuiteTestsEnvironment[TReference, TMetadata]) (PatchedTestSuiteTestsValue[TMetadata], error) {
	targetValue := e.GetTargetValue(&model_analysis_pb.Target_Key{Label: key.Label})
	if !targetValue.IsSet() {
		return PatchedTestSuiteTestsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	definition, ok := targetValue.Message.Definition.GetKind().(*model_starlark_pb.Target_Definition_TestSuite)
	if !ok {
		return PatchedTestSuiteTestsValue[TMetadata]{}, fmt.Errorf("target %#v is not a test suite", key.Label)
	}
	testSuite := definition.TestSuite

	// Tests that are part of the test suite directly are only
	// included if they pass the test suite's filters. Tests that
	// are part of nested test suites are included without applying
	// these filters, as is the case for Bazel.
	tests := labelSet{}
	var nestedTestSuites []model_core.Message[[]*model_analysis_pb.TargetLabel, TReference]
	// Load the targets in batches, so that the number of
	// targets that is loaded into memory at once is
	// bounded. Request all targets before returning
	// ErrMissingDependency, so that they can be loaded
	// concurrently.
	missingDependencies := false
	batch := make([]label.CanonicalLabel, 0, queryStreamingBatchSize)
	flushBatch := func() error {
		for _, testLabel := range batch {
			testLabelStr := testLabel.String()
			testValue := e.GetTargetValue(&model_analysis_pb.Target_Key{Label: testLabelStr})
			if !testValue.IsSet() {
				missingDependencies = true
				continue
			}
			switch testKind := testValue.Message.Definition.GetKind().(type) {
			case *model_starlark_pb.Target_Definition_RuleTarget:
				if ruleTarget := testKind.RuleTarget; ruleTarget.Test != nil {
					if model_starlark.TestSuiteMatchesTest(testSuite, ruleTarget.Tags, ruleTarget.Test.Size) {
						tests[testLabel] = struct{}{}
					}
					continue
				}
			case *model_starlark_pb.Target_Definition_TestSuite:
				nestedTestSuiteTests := e.GetTestSuiteTestsValue(&model_analysis_pb.TestSuiteTests_Key{Label: testLabelStr})
				if !nestedTestSuiteTests.IsSet() {
					missingDependencies = true
					continue
				}
				nestedTestSuites = append(nestedTestSuites, model_core.Nested(nestedTestSuiteTests, nestedTestSuiteTests.Message.TargetLabels))
				continue
			}
			return fmt.Errorf("target %#v is neither a test nor a test suite", testLabelStr)
		}
		batch = batch[:0]
		return nil
	}
	var errIter error
	for testLabelStr := range c.allTestSuiteTests(ctx, model_core.Nested(targetValue, testSuite.Tests), &errIter) {
		testLabel, err := canonicalizeQueryLabel(testLabelStr)
		if err != nil {
			return PatchedTestSuiteTestsValue[TMetadata]{}, err
		}
		batch = append(batch, testLabel)
		if len(batch) == queryStreamingBatchSize {
			if err := flushBatch(); err != nil {
				return PatchedTestSuiteTestsValue[TMetadata]{}, err
			}
		}
	}
	if errIter != nil {
		return PatchedTestSuiteTestsValue[TMetadata]{}, fmt.Errorf("failed to read tests: %w", errIter)
	}
	if err := flushBatch(); err != nil {
		return PatchedTestSuiteTestsValue[TMetadata]{}, err
	}
	if missingDependencies {
		return PatchedTestSuiteTestsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	targetLabels, err := c.mergeTargetLabels(ctx, e, append(nestedTestSuites, newTargetLabelLeaves[TReference](tests)))
	if err != nil {
		return PatchedTestSuiteTestsValue[TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_analysis_pb.TestSuiteTests_Value{
			TargetLabels: targetLabels.Message,
		},
		targetLabels.Patcher,
	), nil
}

// TestSuiteTestsEnvironmentForTesting is used to generate mocks for
// testing ComputeTestSuiteTestsValue().
type TestSuiteTestsEnvironmentForTesting TestSuiteTestsEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
