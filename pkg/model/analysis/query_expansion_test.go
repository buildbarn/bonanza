package analysis_test

import (
	"context"
	"encoding"
	"fmt"
	"maps"
	"math/big"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"bonanza.build/pkg/glob"
	"bonanza.build/pkg/label"
	model_analysis "bonanza.build/pkg/model/analysis"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_format "bonanza.build/pkg/model/format"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"

	"go.uber.org/mock/gomock"
)

// normalizeCanonicalQuery converts a query expression containing
// canonical target patterns to normal form, as is done when
// constructing QueryExpansion keys.
// allPackagesPattern is the pattern that query expansion provides to
// PackagesAtAndBelow to obtain all packages below a base package.
var allPackagesPattern = util.Must(glob.NewNFAFromPatterns([]string{"**"}, nil)).Bytes()

func normalizeCanonicalQuery(query string) string {
	return label.QueryToString(util.Must(label.NewQuery(query, label.NewCanonicalTargetPattern)))
}

func newTargetLabels(labels ...string) []*model_analysis_pb.TargetLabel {
	targetLabels := []*model_analysis_pb.TargetLabel{}
	for _, l := range labels {
		targetLabels = append(targetLabels, model_analysis_pb.TargetLabel_builder{
			Leaf: proto.String(l),
		}.Build())
	}
	return targetLabels
}

func getTargetLabels(t *testing.T, targetLabels []*model_analysis_pb.TargetLabel) []string {
	labels := []string{}
	for _, l := range targetLabels {
		leaf := l.GetLeaf()
		require.NotEmpty(t, leaf)
		labels = append(labels, leaf)
	}
	return labels
}

func newQueryExpansionValue(labels ...string) model_core.Message[*model_analysis_pb.QueryExpansion_Value, model_core.CreatedObjectTree] {
	return model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.QueryExpansion_Value_builder{
		TargetLabels: newTargetLabels(labels...),
	}.Build())
}

func newSourceFileTargetValue() model_core.Message[*model_analysis_pb.Target_Value, model_core.CreatedObjectTree] {
	return model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
		Definition: model_starlark_pb.Target_Definition_builder{
			SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
		}.Build(),
	}.Build())
}

func labelListValue(labels ...string) *model_starlark_pb.Value {
	elements := make([]*model_starlark_pb.List_Element, 0, len(labels))
	for _, l := range labels {
		elements = append(elements, model_starlark_pb.List_Element_builder{
			Leaf: proto.ValueOrDefault(labelValue(l)),
		}.Build())
	}
	return model_starlark_pb.Value_builder{
		List: model_starlark_pb.List_builder{Elements: elements}.Build(),
	}.Build()
}

func stringValue(s string) *model_starlark_pb.Value {
	return newStringValue(s)
}

// newRuleDefinitionValue returns the value of a CompiledBzlFileGlobal
// key that refers to a rule having a given set of attributes.
func newRuleDefinitionValue(ruleType model_starlark_pb.Rule_Definition_Type, attrs ...*model_starlark_pb.NamedAttr) model_core.Message[*model_analysis_pb.CompiledBzlFileGlobal_Value, model_core.CreatedObjectTree] {
	return model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.CompiledBzlFileGlobal_Value_builder{
		Global: model_starlark_pb.Value_builder{
			Rule: model_starlark_pb.Rule_builder{
				Definition: model_starlark_pb.Rule_Definition_builder{
					Attrs: attrs,
					Type:  ruleType,
				}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
}

func valueSelectGroup(v *model_starlark_pb.Value) *model_starlark_pb.Select_Group {
	return model_starlark_pb.Select_Group_builder{
		NoMatchValue: proto.ValueOrDefault(v),
	}.Build()
}

func TestQueryExpansion(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	expectQueryExpansion := func(e *MockQueryExpansionEnvironmentForTesting, query string, value model_core.Message[*model_analysis_pb.QueryExpansion_Value, model_core.CreatedObjectTree]) *gomock.Call {
		return e.EXPECT().GetQueryExpansionValue(
			testutil.EqProto(t, model_analysis_pb.QueryExpansion_Key_builder{Query: query}.Build()),
		).Return(value)
	}
	expectDirectDependencies := func(e *MockQueryExpansionEnvironmentForTesting, dependencies map[string][]string) {
		for l, labels := range dependencies {
			e.EXPECT().GetTargetDirectDependenciesValue(
				testutil.EqProto(t, model_analysis_pb.TargetDirectDependencies_Key_builder{Label: l}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.TargetDirectDependencies_Value_builder{
				TargetLabels: newTargetLabels(labels...),
			}.Build())).AnyTimes()
		}
	}
	computeQueryExpansion := func(e *MockQueryExpansionEnvironmentForTesting, query string) ([]string, error) {
		value, err := bct.computer.ComputeQueryExpansionValue(ctx, model_analysis_pb.QueryExpansion_Key_builder{
			Query: normalizeCanonicalQuery(query),
		}.Build(), e)
		if err != nil {
			return nil, err
		}
		return getTargetLabels(t, value.Message.GetTargetLabels()), nil
	}

	expectPackage := func(e *MockQueryExpansionEnvironmentForTesting, canonicalPackage string, targets ...*model_starlark_pb.Target) {
		value := &model_analysis_pb.PackageTargets_Value{}
		for _, target := range targets {
			value.SetTargets(append(value.GetTargets(), model_analysis_pb.Package_Value_Target_builder{
				Leaf: proto.ValueOrDefault(target),
			}.Build()))
		}
		e.EXPECT().GetPackageTargetsValue(
			testutil.EqProto(t, model_analysis_pb.PackageTargets_Key_builder{Package: canonicalPackage}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](value)).AnyTimes()
	}
	ruleTarget := func(name string, tags ...string) *model_starlark_pb.Target {
		return model_starlark_pb.Target_builder{
			Name: name,
			Definition: model_starlark_pb.Target_Definition_builder{
				RuleTarget: model_starlark_pb.RuleTarget_builder{
					RuleIdentifier: "@@rules+//:defs.bzl%my_rule",
					Tags:           tags,
				}.Build(),
			}.Build(),
		}.Build()
	}
	sourceFileTarget := func(name string) *model_starlark_pb.Target {
		return model_starlark_pb.Target_builder{
			Name: name,
			Definition: model_starlark_pb.Target_Definition_builder{
				SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
			}.Build(),
		}.Build()
	}

	t.Run("SinglePackageTargetPattern", func(t *testing.T) {
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectPackage(e, "@@main+//foo", ruleTarget("a", "manual"), ruleTarget("b"), sourceFileTarget("c.txt"), ruleTarget("foo"))

		t.Run("All", func(t *testing.T) {
			// ":all" excludes file targets. As is the case
			// for "bazel query", targets having tag "manual"
			// are included.
			value, err := bct.computer.ComputeQueryExpansionValue(ctx, model_analysis_pb.QueryExpansion_Key_builder{
				Query: "@@main+//foo:all",
			}.Build(), e)
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//foo:a", "@@main+//foo:b", "@@main+//foo"}, getTargetLabels(t, value.Message.GetTargetLabels()))
		})

		t.Run("IncludeFileTargets", func(t *testing.T) {
			value, err := bct.computer.ComputeQueryExpansionValue(ctx, model_analysis_pb.QueryExpansion_Key_builder{
				Query: "@@main+//foo:*",
			}.Build(), e)
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//foo:a", "@@main+//foo:b", "@@main+//foo:c.txt", "@@main+//foo"}, getTargetLabels(t, value.Message.GetTargetLabels()))
		})
	})

	// readTargetLabels returns all labels contained in the value
	// of QueryExpansion, loading any objects referenced by parent
	// nodes. It also validates that parent nodes contain the
	// correct range of labels, and returns how many parent nodes
	// were encountered.
	targetLabelReader := model_parser.LookupParsedObjectReader(
		bct.parsedObjectPoolIngester,
		model_parser.NewProtoListObjectParser[model_core.CreatedObjectTree, model_analysis_pb.TargetLabel](),
	)
	readTargetLabels := func(t *testing.T, value model_analysis.PatchedQueryExpansionValue[model_core.CreatedObjectTree]) ([]string, int) {
		parentsSeen := 0
		var getLabels func(list model_core.Message[[]*model_analysis_pb.TargetLabel, model_core.CreatedObjectTree]) []string
		getLabels = func(list model_core.Message[[]*model_analysis_pb.TargetLabel, model_core.CreatedObjectTree]) []string {
			var labels []string
			for _, targetLabel := range list.Message {
				switch targetLabel.WhichLevel() {
				case model_analysis_pb.TargetLabel_Leaf_case:
					labels = append(labels, targetLabel.GetLeaf())
				case model_analysis_pb.TargetLabel_Parent_case:
					reference, err := model_core.FlattenDecodableReference(model_core.Nested(list, targetLabel.GetParent().GetReference()))
					require.NoError(t, err)
					children, err := targetLabelReader.ReadObject(ctx, reference)
					require.NoError(t, err)
					childLabels := getLabels(children)
					require.NotEmpty(t, childLabels)
					require.Equal(t, childLabels[0], targetLabel.GetParent().GetFirstLabel())
					require.Equal(t, childLabels[len(childLabels)-1], targetLabel.GetParent().GetLastLabel())
					labels = append(labels, childLabels...)
					parentsSeen++
				default:
					t.Fatal("invalid level type")
				}
			}
			return labels
		}
		sortedValue, metadata := value.SortAndSetReferences()
		labels := getLabels(model_core.NewMessage(
			sortedValue.Message.GetTargetLabels(),
			object.OutgoingReferencesList[model_core.CreatedObjectTree](metadata),
		))
		return labels, parentsSeen
	}
	expectCaptureObjects := func(e *MockQueryExpansionEnvironmentForTesting) {
		e.EXPECT().CaptureCreatedObject(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, createdObject model_core.CreatedObject[model_core.CreatedObjectTree]) (model_core.CreatedObjectTree, error) {
				return model_core.CreatedObjectTree(createdObject), nil
			}).
			AnyTimes()
		e.EXPECT().CaptureExistingObject(gomock.Any()).
			DoAndReturn(func(reference model_core.CreatedObjectTree) model_core.CreatedObjectTree {
				return reference
			}).
			AnyTimes()
	}
	// newTargetLabelsParent creates a parent node of a B-tree of
	// labels, referencing an object containing the provided labels.
	newTargetLabelsParent := func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree], firstLabel, lastLabel string, labels ...string) *model_analysis_pb.TargetLabel {
		return model_analysis_pb.TargetLabel_builder{
			Parent: model_analysis_pb.TargetLabel_Parent_builder{
				Reference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
					return model_format.NewProtoList[model_analysis_pb.TargetLabel]().NewMarshaler(newTargetLabels(labels...))
				})),
				FirstLabel: firstLabel,
				LastLabel:  lastLabel,
			}.Build(),
		}.Build()
	}

	// expectTargetLabels expects a QueryExpansion key to be
	// requested, returning a B-tree of labels.
	expectTargetLabels := func(e *MockQueryExpansionEnvironmentForTesting, query string, build func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel) {
		expectQueryExpansion(e, query, newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.QueryExpansion_Value {
			return model_analysis_pb.QueryExpansion_Value_builder{
				TargetLabels: build(patcher),
			}.Build()
		}))
	}
	leafTargetLabel := func(l string) *model_analysis_pb.TargetLabel {
		return model_analysis_pb.TargetLabel_builder{
			Leaf: proto.String(l),
		}.Build()
	}

	t.Run("LargePackage", func(t *testing.T) {
		// Results that are too large to store in a single
		// object should be stored in a B-tree. Parent nodes
		// should contain the first and last label stored in
		// the objects they reference.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectCaptureObjects(e)
		var targets []*model_starlark_pb.Target
		var expectedLabels []string
		for i := range 10000 {
			name := fmt.Sprintf("target_%05d", i)
			targets = append(targets, ruleTarget(name))
			expectedLabels = append(expectedLabels, "@@main+//large:"+name)
		}
		expectPackage(e, "@@main+//large", targets...)

		value, err := bct.computer.ComputeQueryExpansionValue(ctx, model_analysis_pb.QueryExpansion_Key_builder{
			Query: "@@main+//large:all",
		}.Build(), e)
		require.NoError(t, err)

		labels, parentsSeen := readTargetLabels(t, value)
		require.Equal(t, expectedLabels, labels)
		require.Greater(t, parentsSeen, 1)
	})

	t.Run("RecursiveTargetPattern", func(t *testing.T) {
		// Targets in the base package should be followed by the
		// results of the subpackages, which are requested as
		// separate keys. The result should be sorted, even though
		// "@@main+//foo/bar:y" sorts after "@@main+//foo/bar2:z" when
		// compared as strings.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetPackagesAtAndBelowValue(
			testutil.EqProto(t, model_analysis_pb.PackagesAtAndBelow_Key_builder{BasePackage: "@@main+//foo", Pattern: allPackagesPattern}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.PackagesAtAndBelow_Value_builder{
			PackageAtBasePackage:     true,
			PackagesBelowBasePackage: []string{"bar", "bar2"},
		}.Build()))
		expectPackage(e, "@@main+//foo", ruleTarget("x"))
		expectQueryExpansion(e, "@@main+//foo/bar/...", newQueryExpansionValue("@@main+//foo/bar:y"))
		expectQueryExpansion(e, "@@main+//foo/bar2/...", newQueryExpansionValue("@@main+//foo/bar2:z"))

		labels, err := computeQueryExpansion(e, "@@main+//foo/...")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//foo:x", "@@main+//foo/bar:y", "@@main+//foo/bar2:z"}, labels)
	})

	t.Run("MultipleTargetPatterns", func(t *testing.T) {
		// Each target pattern in a set should be expanded using a
		// separate key, so that results can be reused.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//foo/...", newQueryExpansionValue("@@main+//foo:x", "@@main+//foo/bar:y"))
		expectQueryExpansion(e, "@@main+//bar:all", newQueryExpansionValue("@@main+//bar:z"))

		labels, err := computeQueryExpansion(e, "set(@@main+//foo/... @@main+//bar:all)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//bar:z", "@@main+//foo:x", "@@main+//foo/bar:y"}, labels)
	})

	t.Run("ImplicitDepsForTargetPatterns", func(t *testing.T) {
		// Implicit dependencies have no effect on queries that
		// don't follow dependencies, so keys should not request
		// them.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		for _, query := range []string{
			"@@main+//foo/...",
			"kind(rule, @@main+//foo:all)",
		} {
			_, err := bct.computer.ComputeQueryExpansionValue(ctx, model_analysis_pb.QueryExpansion_Key_builder{
				Query:               query,
				IncludeImplicitDeps: true,
			}.Build(), e)
			require.EqualError(t, err, "implicit dependencies cannot be included, as they do not affect the results of the query")
		}
	})

	t.Run("PerPackage", func(t *testing.T) {
		// Queries consisting of a single recursive target pattern,
		// set operations and functions that process targets
		// individually should be evaluated for each package
		// separately. Results for the base package and the
		// subpackages are requested as separate keys. For the
		// base package, the target pattern is replaced by a
		// query yielding the targets in the package.
		expectPackagesAtAndBelow := func(e *MockQueryExpansionEnvironmentForTesting, packageAtBasePackage bool) {
			e.EXPECT().GetPackagesAtAndBelowValue(
				testutil.EqProto(t, model_analysis_pb.PackagesAtAndBelow_Key_builder{BasePackage: "@@main+//foo", Pattern: allPackagesPattern}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.PackagesAtAndBelow_Value_builder{
				PackageAtBasePackage:     packageAtBasePackage,
				PackagesBelowBasePackage: []string{"bar"},
			}.Build()))
		}

		t.Run("Kind", func(t *testing.T) {
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectPackagesAtAndBelow(e, true)
			expectQueryExpansion(e, "kind(rule, rules(siblings(@@main+//foo:*)))", newQueryExpansionValue("@@main+//foo:x"))
			expectQueryExpansion(e, "kind(rule, @@main+//foo/bar/...)", newQueryExpansionValue("@@main+//foo/bar:z"))

			labels, err := computeQueryExpansion(e, "kind(rule, @@main+//foo/...)")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//foo:x", "@@main+//foo/bar:z"}, labels)
		})

		t.Run("IncludeFileTargets", func(t *testing.T) {
			// If file targets are included, there is no
			// need to filter the targets in the base
			// package using rules().
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectPackagesAtAndBelow(e, true)
			expectQueryExpansion(e, "packages(siblings(@@main+//foo:*))", newQueryExpansionValue("@@main+//foo"))
			expectQueryExpansion(e, "packages(@@main+//foo/bar/...:*)", newQueryExpansionValue("@@main+//foo/bar"))

			labels, err := computeQueryExpansion(e, "packages(@@main+//foo/...:*)")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//foo", "@@main+//foo/bar"}, labels)
		})

		t.Run("ExcludeTag", func(t *testing.T) {
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectPackagesAtAndBelow(e, true)
			expectQueryExpansion(e, normalizeCanonicalQuery("rules(siblings(@@main+//foo:*)) - tag(manual, rules(siblings(@@main+//foo:*)))"), newQueryExpansionValue("@@main+//foo:x"))
			expectQueryExpansion(e, normalizeCanonicalQuery("@@main+//foo/bar/... - tag(manual, @@main+//foo/bar/...)"), newQueryExpansionValue("@@main+//foo/bar:z"))

			labels, err := computeQueryExpansion(e, "@@main+//foo/... - tag(manual, @@main+//foo/...)")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//foo:x", "@@main+//foo/bar:z"}, labels)
		})

		t.Run("NoBasePackage", func(t *testing.T) {
			// If the base package does not exist, only the
			// subpackages should be evaluated.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectPackagesAtAndBelow(e, false)
			expectQueryExpansion(e, "kind(rule, @@main+//foo/bar/...)", newQueryExpansionValue("@@main+//foo/bar:z"))

			labels, err := computeQueryExpansion(e, "kind(rule, @@main+//foo/...)")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//foo/bar:z"}, labels)
		})

		t.Run("MissingDependencies", func(t *testing.T) {
			// Subpackages should still be requested if the
			// results of the base package are not available
			// yet.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectPackagesAtAndBelow(e, true)
			expectQueryExpansion(e, "kind(rule, rules(siblings(@@main+//foo:*)))", model_core.Message[*model_analysis_pb.QueryExpansion_Value, model_core.CreatedObjectTree]{})
			expectQueryExpansion(e, "kind(rule, @@main+//foo/bar/...)", newQueryExpansionValue("@@main+//foo/bar:z"))

			_, err := computeQueryExpansion(e, "kind(rule, @@main+//foo/...)")
			require.ErrorIs(t, err, evaluation.ErrMissingDependency)
		})

		t.Run("NotSplittable", func(t *testing.T) {
			// Queries involving other target patterns can't
			// be evaluated for each package separately.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectQueryExpansion(e, "@@main+//foo/...", newQueryExpansionValue("@@main+//foo:x", "@@main+//foo/bar:z"))
			expectQueryExpansion(e, "@@main+//foo:x", newQueryExpansionValue("@@main+//foo:x"))

			labels, err := computeQueryExpansion(e, "@@main+//foo/... - @@main+//foo:x")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//foo/bar:z"}, labels)
		})
	})

	t.Run("SinglePackage", func(t *testing.T) {
		// Queries that only process the targets yielded by
		// siblings() for a single package should be evaluated in
		// memory, without requesting separate keys for their
		// subexpressions.
		expectTarget := func(e *MockQueryExpansionEnvironmentForTesting, target *model_starlark_pb.Target) {
			e.EXPECT().GetTargetValue(
				testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//foo:" + target.GetName()}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
				Definition: target.GetDefinition(),
			}.Build())).AnyTimes()
		}

		t.Run("ExcludeTag", func(t *testing.T) {
			// rules() should exclude file targets.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			x, y, z := ruleTarget("x"), ruleTarget("y", "manual"), sourceFileTarget("z")
			expectPackage(e, "@@main+//foo", x, y, z)
			expectTarget(e, x)
			expectTarget(e, y)
			expectTarget(e, z)

			labels, err := computeQueryExpansion(e, "rules(siblings(@@main+//foo:*)) - tag(manual, rules(siblings(@@main+//foo:*)))")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//foo:x"}, labels)
		})

		t.Run("RulesWithoutLoadingTargets", func(t *testing.T) {
			// rules(siblings(//foo:*)) can be evaluated
			// using the definitions stored in the package.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectPackage(e, "@@main+//foo", ruleTarget("x"), sourceFileTarget("y"))

			labels, err := computeQueryExpansion(e, "rules(siblings(@@main+//foo:*))")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//foo:x"}, labels)
		})

		t.Run("Tests", func(t *testing.T) {
			// tests() can't be evaluated in memory, as test
			// suites may refer to tests in other packages.
			// The results of its operand should be
			// requested separately.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			x, y := ruleTarget("x"), sourceFileTarget("y")
			x.GetDefinition().GetRuleTarget().SetTest(&model_starlark_pb.RuleTarget_Test{})
			expectQueryExpansion(e, "siblings(@@main+//foo:*)", newQueryExpansionValue("@@main+//foo:x", "@@main+//foo:y"))
			expectTarget(e, x)
			expectTarget(e, y)

			labels, err := computeQueryExpansion(e, "tests(siblings(@@main+//foo:*))")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//foo:x"}, labels)
		})

		t.Run("Packages", func(t *testing.T) {
			// Targets don't need to be loaded to compute
			// the labels of their packages.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectPackage(e, "@@main+//foo", ruleTarget("x"), sourceFileTarget("y"))

			labels, err := computeQueryExpansion(e, "packages(siblings(@@main+//foo:*))")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//foo"}, labels)
		})

		t.Run("MissingPackage", func(t *testing.T) {
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			e.EXPECT().GetPackageTargetsValue(
				testutil.EqProto(t, model_analysis_pb.PackageTargets_Key_builder{Package: "@@main+//foo"}.Build()),
			).Return(model_core.Message[*model_analysis_pb.PackageTargets_Value, model_core.CreatedObjectTree]{})

			_, err := computeQueryExpansion(e, "rules(siblings(@@main+//foo:*))")
			require.ErrorIs(t, err, evaluation.ErrMissingDependency)
		})

		t.Run("SiblingsUsedTwice", func(t *testing.T) {
			// Queries that use siblings() multiple times
			// can't be evaluated as a single package.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectQueryExpansion(e, "siblings(@@main+//foo:*)", newQueryExpansionValue("@@main+//foo:x"))
			expectQueryExpansion(e, "siblings(@@main+//bar:*)", newQueryExpansionValue("@@main+//bar:y"))

			labels, err := computeQueryExpansion(e, "siblings(@@main+//foo:*) + siblings(@@main+//bar:*)")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//bar:y", "@@main+//foo:x"}, labels)
		})
	})

	t.Run("Siblings", func(t *testing.T) {
		t.Run("Wildcards", func(t *testing.T) {
			// If the operand only contains target patterns
			// matching all targets in a package, the
			// packages should be read directly, without
			// expanding the target patterns. This is
			// correct even if a package contains a target
			// named "*", which "@@main+//a:*" would match
			// exclusively.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectPackage(e, "@@main+//a", ruleTarget("*"), sourceFileTarget("x"))
			expectPackage(e, "@@main+//b", ruleTarget("y"))

			labels, err := computeQueryExpansion(e, "siblings(set(@@main+//a:* @@main+//b:all-targets))")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//a:*", "@@main+//a:x", "@@main+//b:y"}, labels)
		})

		t.Run("OtherTargetPatterns", func(t *testing.T) {
			// "@@main+//a:all" yields no targets if a
			// package only contains file targets, meaning
			// it needs to be expanded.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectQueryExpansion(e, "@@main+//a:all", newQueryExpansionValue("@@main+//a:y"))
			expectPackage(e, "@@main+//a", sourceFileTarget("x"), ruleTarget("y"))

			labels, err := computeQueryExpansion(e, "siblings(@@main+//a:all)")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//a:x", "@@main+//a:y"}, labels)
		})
	})

	t.Run("Rules", func(t *testing.T) {
		// Rule targets and targets that behave like rules should
		// be matched. File targets and package groups should not.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//:*", newQueryExpansionValue("@@main+//:a", "@@main+//:b", "@@main+//:c", "@@main+//:d", "@@main+//:e"))
		expectTarget := func(l string, definition *model_starlark_pb.Target_Definition) {
			e.EXPECT().GetTargetValue(
				testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: l}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
				Definition: definition,
			}.Build()))
		}
		expectTarget("@@main+//:a", ruleTarget("a").GetDefinition())
		expectTarget("@@main+//:b", model_starlark_pb.Target_Definition_builder{
			Alias: &model_starlark_pb.Alias{},
		}.Build())
		expectTarget("@@main+//:c", model_starlark_pb.Target_Definition_builder{
			LabelSetting: &model_starlark_pb.LabelSetting{},
		}.Build())
		expectTarget("@@main+//:d", sourceFileTarget("d").GetDefinition())
		expectTarget("@@main+//:e", model_starlark_pb.Target_Definition_builder{
			PackageGroup: &model_starlark_pb.PackageGroup{},
		}.Build())

		labels, err := computeQueryExpansion(e, "rules(@@main+//:*)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:a", "@@main+//:b", "@@main+//:c"}, labels)
	})

	t.Run("RulesOfIdentifier", func(t *testing.T) {
		// If a rule identifier is provided, only instances of
		// that exact rule should be matched. Aliases should not
		// be followed.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//:*", newQueryExpansionValue("@@main+//:a", "@@main+//:b", "@@main+//:c", "@@main+//:d"))
		expectTarget := func(l string, definition *model_starlark_pb.Target_Definition) {
			e.EXPECT().GetTargetValue(
				testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: l}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
				Definition: definition,
			}.Build()))
		}
		expectTarget("@@main+//:a", ruleTarget("a").GetDefinition())
		expectTarget("@@main+//:b", model_starlark_pb.Target_Definition_builder{
			RuleTarget: model_starlark_pb.RuleTarget_builder{
				RuleIdentifier: "@@rules+//:defs.bzl%other_rule",
			}.Build(),
		}.Build())
		expectTarget("@@main+//:c", model_starlark_pb.Target_Definition_builder{
			Alias: &model_starlark_pb.Alias{},
		}.Build())
		expectTarget("@@main+//:d", sourceFileTarget("d").GetDefinition())

		labels, err := computeQueryExpansion(e, "rules(@@main+//:*, @@rules+//:defs.bzl%my_rule)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:a"}, labels)
	})

	t.Run("NotInNormalForm", func(t *testing.T) {
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		_, err := bct.computer.ComputeQueryExpansionValue(ctx, model_analysis_pb.QueryExpansion_Key_builder{
			Query: "@@main+//:a union @@main+//:b",
		}.Build(), e)
		require.EqualError(t, err, "query is not in normal form, which is \"@@main+//:a + @@main+//:b\"")
	})

	t.Run("SingleTarget", func(t *testing.T) {
		// Single targets are checked for existence.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:b"}.Build()),
		).Return(newSourceFileTargetValue())
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:a"}.Build()),
		).Return(newSourceFileTargetValue())

		labels, err := computeQueryExpansion(e, "set(@@main+//:b @@main+//:a)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:a", "@@main+//:b"}, labels)
	})

	t.Run("SetOperation", func(t *testing.T) {
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "set(@@main+//:a @@main+//:b)", newQueryExpansionValue("@@main+//:a", "@@main+//:b"))
		expectQueryExpansion(e, "deps(@@main+//:c)", newQueryExpansionValue("@@main+//:b", "@@main+//:c"))

		labels, err := computeQueryExpansion(e, "set(@@main+//:a @@main+//:b) - deps(@@main+//:c)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:a"}, labels)
	})

	t.Run("Union", func(t *testing.T) {
		// Unions are computed by merging the B-trees of the
		// results of the operands.
		t.Run("Disjoint", func(t *testing.T) {
			// Nodes that don't overlap with the other
			// operand should be copied as is, without
			// loading the objects they reference.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectQueryExpansion(e, "@@main+//b/...", newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.QueryExpansion_Value {
				return model_analysis_pb.QueryExpansion_Value_builder{
					TargetLabels: []*model_analysis_pb.TargetLabel{
						newTargetLabelsParent(patcher, "@@main+//b:1", "@@main+//b:3", "@@main+//b:1", "@@main+//b:2", "@@main+//b:3"),
						newTargetLabelsParent(patcher, "@@main+//b/c:1", "@@main+//b/c:2", "@@main+//b/c:1", "@@main+//b/c:2"),
					},
				}.Build()
			}))
			expectQueryExpansion(e, "@@main+//a/...", newQueryExpansionValue("@@main+//a:1", "@@main+//a/b:2"))
			expectQueryExpansion(e, "@@main+//d/...", newQueryExpansionValue("@@main+//d:1"))

			value, err := bct.computer.ComputeQueryExpansionValue(ctx, model_analysis_pb.QueryExpansion_Key_builder{
				Query: normalizeCanonicalQuery("@@main+//b/... + @@main+//a/... + @@main+//d/..."),
			}.Build(), e)
			require.NoError(t, err)
			labels, parentsSeen := readTargetLabels(t, value)
			require.Equal(t, []string{
				"@@main+//a:1",
				"@@main+//a/b:2",
				"@@main+//b:1",
				"@@main+//b:2",
				"@@main+//b:3",
				"@@main+//b/c:1",
				"@@main+//b/c:2",
				"@@main+//d:1",
			}, labels)
			require.Equal(t, 2, parentsSeen)
		})

		t.Run("Overlapping", func(t *testing.T) {
			// Nodes that overlap with the other operand
			// should be descended into. Labels that are
			// present in both operands should only be
			// returned once.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectQueryExpansion(e, "@@main+//a/...", newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.QueryExpansion_Value {
				return model_analysis_pb.QueryExpansion_Value_builder{
					TargetLabels: []*model_analysis_pb.TargetLabel{
						newTargetLabelsParent(patcher, "@@main+//a:1", "@@main+//a:5", "@@main+//a:1", "@@main+//a:3", "@@main+//a:5"),
						newTargetLabelsParent(patcher, "@@main+//a:7", "@@main+//a:9", "@@main+//a:7", "@@main+//a:9"),
					},
				}.Build()
			}))
			expectQueryExpansion(e, "@@main+//a:*", newQueryExpansionValue("@@main+//a:2", "@@main+//a:3", "@@main+//a:6"))

			value, err := bct.computer.ComputeQueryExpansionValue(ctx, model_analysis_pb.QueryExpansion_Key_builder{
				Query: normalizeCanonicalQuery("@@main+//a/... + @@main+//a:*"),
			}.Build(), e)
			require.NoError(t, err)
			labels, parentsSeen := readTargetLabels(t, value)
			require.Equal(t, []string{
				"@@main+//a:1",
				"@@main+//a:2",
				"@@main+//a:3",
				"@@main+//a:5",
				"@@main+//a:6",
				"@@main+//a:7",
				"@@main+//a:9",
			}, labels)
			// Only the second parent node does not
			// overlap, meaning it can be copied as is.
			require.Equal(t, 1, parentsSeen)
		})

		t.Run("IdenticalParents", func(t *testing.T) {
			// If both operands contain identical parent
			// nodes, they contain the same labels. They
			// should be copied once, without descending
			// into them.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			for _, query := range []string{"@@main+//a/...", "@@main+//a:*"} {
				expectTargetLabels(e, query, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
					return []*model_analysis_pb.TargetLabel{
						newTargetLabelsParent(patcher, "@@main+//a:1", "@@main+//a:2", "@@main+//a:1", "@@main+//a:2"),
					}
				})
			}

			value, err := bct.computer.ComputeQueryExpansionValue(ctx, model_analysis_pb.QueryExpansion_Key_builder{
				Query: normalizeCanonicalQuery("@@main+//a/... + @@main+//a:*"),
			}.Build(), e)
			require.NoError(t, err)
			labels, parentsSeen := readTargetLabels(t, value)
			require.Equal(t, []string{"@@main+//a:1", "@@main+//a:2"}, labels)
			require.Equal(t, 1, parentsSeen)
		})

		t.Run("IdenticalParentsOverlappingOtherOperand", func(t *testing.T) {
			// If identical parent nodes overlap with a
			// third operand, they should be descended into,
			// so that duplicate labels are removed.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			for _, query := range []string{"@@main+//a/...", "@@main+//a:*"} {
				expectTargetLabels(e, query, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
					return []*model_analysis_pb.TargetLabel{
						newTargetLabelsParent(patcher, "@@main+//a:1", "@@main+//a:3", "@@main+//a:1", "@@main+//a:3"),
					}
				})
			}
			expectQueryExpansion(e, "@@main+//a:all", newQueryExpansionValue("@@main+//a:2", "@@main+//a:3"))

			value, err := bct.computer.ComputeQueryExpansionValue(ctx, model_analysis_pb.QueryExpansion_Key_builder{
				Query: normalizeCanonicalQuery("@@main+//a/... + @@main+//a:* + @@main+//a:all"),
			}.Build(), e)
			require.NoError(t, err)
			labels, parentsSeen := readTargetLabels(t, value)
			require.Equal(t, []string{"@@main+//a:1", "@@main+//a:2", "@@main+//a:3"}, labels)
			require.Equal(t, 0, parentsSeen)
		})

		t.Run("ParentWithoutRange", func(t *testing.T) {
			// Parent nodes that lack the range of labels
			// they contain should be rejected.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectQueryExpansion(e, "@@main+//a/...", newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.QueryExpansion_Value {
				return model_analysis_pb.QueryExpansion_Value_builder{
					TargetLabels: []*model_analysis_pb.TargetLabel{
						newTargetLabelsParent(patcher, "", "", "@@main+//a:1", "@@main+//a:2"),
					},
				}.Build()
			}))
			expectQueryExpansion(e, "@@main+//a:*", newQueryExpansionValue("@@main+//a:1"))

			_, err := bct.computer.ComputeQueryExpansionValue(ctx, model_analysis_pb.QueryExpansion_Key_builder{
				Query: normalizeCanonicalQuery("@@main+//a/... + @@main+//a:*"),
			}.Build(), e)
			require.ErrorContains(t, err, "invalid first label \"\": ")
		})
	})

	// Parent nodes that reference an object containing an invalid
	// label. These can be used to assert that set operations don't
	// descend into nodes, as doing so causes them to fail. The
	// invalid label is derived from the range, so that parent nodes
	// with different ranges reference different objects.
	newUnloadableTargetLabelsParent := func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree], firstLabel, lastLabel string) *model_analysis_pb.TargetLabel {
		return newTargetLabelsParent(patcher, firstLabel, lastLabel, "invalid "+firstLabel)
	}
	computeSetOperation := func(t *testing.T, e *MockQueryExpansionEnvironmentForTesting, query string) ([]string, int) {
		value, err := bct.computer.ComputeQueryExpansionValue(ctx, model_analysis_pb.QueryExpansion_Key_builder{
			Query: normalizeCanonicalQuery(query),
		}.Build(), e)
		require.NoError(t, err)
		return readTargetLabels(t, value)
	}

	t.Run("Intersect", func(t *testing.T) {
		// Intersections are computed by merging the B-trees of
		// the results of the operands.
		t.Run("Disjoint", func(t *testing.T) {
			// Nodes that don't overlap with the other
			// operand should be skipped, without loading
			// the objects they reference.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectTargetLabels(e, "@@main+//a/...", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
				return []*model_analysis_pb.TargetLabel{
					newUnloadableTargetLabelsParent(patcher, "@@main+//a:1", "@@main+//a:3"),
					leafTargetLabel("@@main+//c:1"),
				}
			})
			expectTargetLabels(e, "@@main+//b/...", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
				return []*model_analysis_pb.TargetLabel{
					newUnloadableTargetLabelsParent(patcher, "@@main+//b:1", "@@main+//b:2"),
					leafTargetLabel("@@main+//c:1"),
					newUnloadableTargetLabelsParent(patcher, "@@main+//d:1", "@@main+//d:2"),
				}
			})

			labels, parentsSeen := computeSetOperation(t, e, "@@main+//a/... ^ @@main+//b/...")
			require.Equal(t, []string{"@@main+//c:1"}, labels)
			require.Equal(t, 0, parentsSeen)
		})

		t.Run("Overlapping", func(t *testing.T) {
			// Nodes that overlap with the other operand
			// should be descended into.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectTargetLabels(e, "@@main+//a/...", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
				return []*model_analysis_pb.TargetLabel{
					newTargetLabelsParent(patcher, "@@main+//a:1", "@@main+//a:5", "@@main+//a:1", "@@main+//a:3", "@@main+//a:5"),
					newUnloadableTargetLabelsParent(patcher, "@@main+//a:7", "@@main+//a:9"),
				}
			})
			expectQueryExpansion(e, "@@main+//a:*", newQueryExpansionValue("@@main+//a:2", "@@main+//a:3", "@@main+//a:5", "@@main+//a:6"))

			labels, parentsSeen := computeSetOperation(t, e, "@@main+//a/... ^ @@main+//a:*")
			require.Equal(t, []string{"@@main+//a:3", "@@main+//a:5"}, labels)
			require.Equal(t, 0, parentsSeen)
		})

		t.Run("IdenticalParents", func(t *testing.T) {
			// Identical parent nodes should be copied as
			// is.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectTargetLabels(e, "@@main+//a/...", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
				return []*model_analysis_pb.TargetLabel{
					newTargetLabelsParent(patcher, "@@main+//a:1", "@@main+//a:2", "@@main+//a:1", "@@main+//a:2"),
					leafTargetLabel("@@main+//a:3"),
				}
			})
			expectTargetLabels(e, "@@main+//a:*", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
				return []*model_analysis_pb.TargetLabel{
					newTargetLabelsParent(patcher, "@@main+//a:1", "@@main+//a:2", "@@main+//a:1", "@@main+//a:2"),
				}
			})

			labels, parentsSeen := computeSetOperation(t, e, "@@main+//a/... ^ @@main+//a:*")
			require.Equal(t, []string{"@@main+//a:1", "@@main+//a:2"}, labels)
			require.Equal(t, 1, parentsSeen)
		})
	})

	t.Run("Except", func(t *testing.T) {
		// Differences are computed by merging the B-trees of
		// the results of the operands.
		t.Run("Disjoint", func(t *testing.T) {
			// Nodes of the left operand that don't overlap
			// with the right operand should be copied as
			// is. Nodes of the right operand that don't
			// overlap with the left operand should be
			// skipped. In both cases the objects they
			// reference should not be loaded.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectTargetLabels(e, "@@main+//a/...", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
				return []*model_analysis_pb.TargetLabel{
					newTargetLabelsParent(patcher, "@@main+//a:1", "@@main+//a:3", "@@main+//a:1", "@@main+//a:2", "@@main+//a:3"),
					newTargetLabelsParent(patcher, "@@main+//c:1", "@@main+//c:2", "@@main+//c:1", "@@main+//c:2"),
				}
			})
			expectTargetLabels(e, "deps(@@main+//b)", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
				return []*model_analysis_pb.TargetLabel{
					newUnloadableTargetLabelsParent(patcher, "@@main+//b:1", "@@main+//b:2"),
					newUnloadableTargetLabelsParent(patcher, "@@main+//d:1", "@@main+//d:2"),
				}
			})

			labels, parentsSeen := computeSetOperation(t, e, "@@main+//a/... - deps(@@main+//b)")
			require.Equal(t, []string{
				"@@main+//a:1",
				"@@main+//a:2",
				"@@main+//a:3",
				"@@main+//c:1",
				"@@main+//c:2",
			}, labels)
			require.Equal(t, 2, parentsSeen)
		})

		t.Run("Overlapping", func(t *testing.T) {
			// Nodes that overlap with the other operand
			// should be descended into.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectTargetLabels(e, "@@main+//a/...", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
				return []*model_analysis_pb.TargetLabel{
					newTargetLabelsParent(patcher, "@@main+//a:1", "@@main+//a:5", "@@main+//a:1", "@@main+//a:3", "@@main+//a:5"),
					newTargetLabelsParent(patcher, "@@main+//a:7", "@@main+//a:9", "@@main+//a:7", "@@main+//a:9"),
				}
			})
			expectQueryExpansion(e, "@@main+//a:*", newQueryExpansionValue("@@main+//a:3", "@@main+//a:6"))

			labels, parentsSeen := computeSetOperation(t, e, "@@main+//a/... - @@main+//a:*")
			require.Equal(t, []string{"@@main+//a:1", "@@main+//a:5", "@@main+//a:7", "@@main+//a:9"}, labels)
			// Only the second parent node does not
			// overlap, meaning it can be copied as is.
			require.Equal(t, 1, parentsSeen)
		})

		t.Run("IdenticalParents", func(t *testing.T) {
			// Identical parent nodes should be skipped,
			// without loading the objects they reference.
			// This allows excluding the results of
			// subpackages from queries against recursive
			// target patterns cheaply, as the B-trees share
			// these nodes.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectTargetLabels(e, "kind(rule, @@main+//a/...)", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
				return []*model_analysis_pb.TargetLabel{
					leafTargetLabel("@@main+//a:1"),
					newUnloadableTargetLabelsParent(patcher, "@@main+//a/b:1", "@@main+//a/b:2"),
				}
			})
			expectTargetLabels(e, "kind(rule, @@main+//a/b/...)", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
				return []*model_analysis_pb.TargetLabel{
					newUnloadableTargetLabelsParent(patcher, "@@main+//a/b:1", "@@main+//a/b:2"),
				}
			})

			labels, parentsSeen := computeSetOperation(t, e, "kind(rule, @@main+//a/...) - kind(rule, @@main+//a/b/...)")
			require.Equal(t, []string{"@@main+//a:1"}, labels)
			require.Equal(t, 0, parentsSeen)
		})
	})

	t.Run("ExcludedDirectories", func(t *testing.T) {
		// Subtracting recursive target patterns from queries
		// that can be evaluated for each package separately
		// should cause the corresponding directories to be
		// skipped, as opposed to being traversed.
		expectPackagesAtAndBelow := func(e *MockQueryExpansionEnvironmentForTesting, basePackage string, packageAtBasePackage bool, packagesBelowBasePackage ...string) {
			e.EXPECT().GetPackagesAtAndBelowValue(
				testutil.EqProto(t, model_analysis_pb.PackagesAtAndBelow_Key_builder{BasePackage: basePackage, Pattern: allPackagesPattern}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.PackagesAtAndBelow_Value_builder{
				PackageAtBasePackage:     packageAtBasePackage,
				PackagesBelowBasePackage: packagesBelowBasePackage,
			}.Build()))
		}

		t.Run("Subpackage", func(t *testing.T) {
			// Subpackages in the excluded directory should
			// not be requested. Other subpackages should be
			// requested using the original query, so that
			// their results can be shared.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectPackagesAtAndBelow(e, "@@main+", true, "a", "b", "c/d")
			expectQueryExpansion(e, "rules(siblings(@@main+//:*))", newQueryExpansionValue("@@main+//:x"))
			expectQueryExpansion(e, "@@main+//a/...", newQueryExpansionValue("@@main+//a:x"))
			expectQueryExpansion(e, "@@main+//c/d/...", newQueryExpansionValue("@@main+//c/d:x"))

			labels, err := computeQueryExpansion(e, "@@main+//... - @@main+//b/...")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//:x", "@@main+//a:x", "@@main+//c/d:x"}, labels)
		})

		t.Run("NestedDirectory", func(t *testing.T) {
			// Subpackages containing an excluded directory
			// should be requested with the exclusion
			// applied. Exclusions that are contained in
			// other exclusions should be omitted, and
			// exclusions should be sorted.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectPackagesAtAndBelow(e, "@@main+", false, "a", "b", "c")
			expectQueryExpansion(e, "@@main+//a/... - @@main+//a/x/... - @@main+//a/y/...", newQueryExpansionValue("@@main+//a:x"))
			expectQueryExpansion(e, "@@main+//b/...", newQueryExpansionValue("@@main+//b:x"))

			labels, err := computeQueryExpansion(e, "@@main+//... - set(@@main+//c/... @@main+//a/y/...) - (@@main+//a/x/... + @@main+//a/x/z/...)")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//a:x", "@@main+//b:x"}, labels)
		})

		t.Run("TagFilters", func(t *testing.T) {
			// Exclusions should also work for queries
			// created by NewQueryFromTargetPatternArguments().
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectPackagesAtAndBelow(e, "@@main+", false, "a", "b")
			expectQueryExpansion(e, normalizeCanonicalQuery("tag(x, @@main+//a/...) - tag(manual, @@main+//a/...)"), newQueryExpansionValue("@@main+//a:x"))

			labels, err := computeQueryExpansion(e, "tag(x, @@main+//...) - tag(manual, @@main+//...) - @@main+//b/...")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//a:x"}, labels)
		})

		t.Run("BaseExcluded", func(t *testing.T) {
			// If the base package is excluded, nothing
			// needs to be evaluated.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			for _, query := range []string{
				"@@main+//... - @@main+//...",
				"@@main+//...:* - @@main+//...:*",
				"tag(x, @@main+//a/...) - @@main+//...",
			} {
				labels, err := computeQueryExpansion(e, query)
				require.NoError(t, err)
				require.Empty(t, labels, query)
			}
		})

		t.Run("Unrelated", func(t *testing.T) {
			// Exclusions that are not below the base
			// package have no effect.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectQueryExpansion(e, "@@main+//a/...", newQueryExpansionValue("@@main+//a:x"))

			labels, err := computeQueryExpansion(e, "@@main+//a/... - @@main+//b/... - @@other+//a/...")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//a:x"}, labels)
		})

		t.Run("FileTargetsNotExcluded", func(t *testing.T) {
			// If the query yields file targets while the
			// exclusion does not, directories can't be
			// skipped.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectQueryExpansion(e, "@@main+//...:*", newQueryExpansionValue("@@main+//b:file", "@@main+//b:rule"))
			expectQueryExpansion(e, "@@main+//b/...", newQueryExpansionValue("@@main+//b:rule"))

			labels, err := computeQueryExpansion(e, "@@main+//...:* - @@main+//b/...")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//b:file"}, labels)
		})

		t.Run("Packages", func(t *testing.T) {
			// packages() yields labels that are not
			// necessarily matched by the excluded target
			// patterns.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectQueryExpansion(e, "packages(@@main+//...)", newQueryExpansionValue("@@main+//b"))
			expectQueryExpansion(e, "@@main+//b/...", newQueryExpansionValue("@@main+//b:x"))

			labels, err := computeQueryExpansion(e, "packages(@@main+//...) - @@main+//b/...")
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//b"}, labels)
		})
	})

	t.Run("SetOperationsRandomized", func(t *testing.T) {
		// Compare the results of set operations against ones
		// computed using plain sets, for B-trees whose labels
		// are randomly spread across parent nodes. As labels
		// are drawn from a small universe, operands frequently
		// share identical parent nodes.
		random := rand.New(rand.NewPCG(1, 2))
		universe := make([]string, 12)
		for i := range universe {
			universe[i] = fmt.Sprintf("@@main+//a:%02d", i)
		}
		randomTargetLabels := func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree], labels []string) []*model_analysis_pb.TargetLabel {
			var targetLabels []*model_analysis_pb.TargetLabel
			for len(labels) > 0 {
				n := min(len(labels), 1+random.IntN(4))
				if random.IntN(2) == 0 {
					targetLabels = append(targetLabels, newTargetLabelsParent(patcher, labels[0], labels[n-1], labels[:n]...))
				} else {
					for _, l := range labels[:n] {
						targetLabels = append(targetLabels, leafTargetLabel(l))
					}
				}
				labels = labels[n:]
			}
			return targetLabels
		}
		for range 500 {
			var left, right []string
			leftSet, rightSet := map[string]bool{}, map[string]bool{}
			for _, l := range universe {
				if random.IntN(3) > 0 {
					left = append(left, l)
					leftSet[l] = true
				}
				if random.IntN(3) > 0 {
					right = append(right, l)
					rightSet[l] = true
				}
			}
			for _, operator := range []string{"+", "^", "-"} {
				e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
				e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
				e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
				e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
				expectCaptureObjects(e)
				expectTargetLabels(e, "@@main+//a/...", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
					return randomTargetLabels(patcher, left)
				})
				expectTargetLabels(e, "@@main+//a:*", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
					return randomTargetLabels(patcher, right)
				})

				expectedLabels := []string{}
				for _, l := range universe {
					if (operator == "+" && (leftSet[l] || rightSet[l])) ||
						(operator == "^" && leftSet[l] && rightSet[l]) ||
						(operator == "-" && leftSet[l] && !rightSet[l]) {
						expectedLabels = append(expectedLabels, l)
					}
				}
				labels, _ := computeSetOperation(t, e, "@@main+//a/... "+operator+" @@main+//a:*")
				if labels == nil {
					labels = []string{}
				}
				require.Equal(t, expectedLabels, labels, "%v %s %v", left, operator, right)
			}
		}
	})

	t.Run("MissingOperands", func(t *testing.T) {
		// Both operands should be requested, even if the first
		// one is not available yet.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "deps(@@main+//:a)", model_core.Message[*model_analysis_pb.QueryExpansion_Value, model_core.CreatedObjectTree]{})
		expectQueryExpansion(e, "deps(@@main+//:b)", newQueryExpansionValue("@@main+//:b"))

		_, err := computeQueryExpansion(e, "deps(@@main+//:a) ^ deps(@@main+//:b)")
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})

	t.Run("Deps", func(t *testing.T) {
		// With a maximum depth of two, the dependencies of the
		// targets at depth two should not be requested.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//:a", newQueryExpansionValue("@@main+//:a"))
		expectDirectDependencies(e, map[string][]string{
			"@@main+//:a": {"@@main+//:b"},
			"@@main+//:b": {"@@main+//:a", "@@main+//:c"},
		})

		labels, err := computeQueryExpansion(e, "deps(@@main+//:a, 2)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:a", "@@main+//:b", "@@main+//:c"}, labels)
	})

	t.Run("ReverseDeps", func(t *testing.T) {
		// Within the universe, :a depends on :b, which depends
		// on :c. :x depends on nothing.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//...", newQueryExpansionValue("@@main+//:a", "@@main+//:x")).Times(2)
		expectQueryExpansion(e, "@@main+//:c", newQueryExpansionValue("@@main+//:c")).Times(2)
		expectDirectDependencies(e, map[string][]string{
			"@@main+//:a": {"@@main+//:b"},
			"@@main+//:b": {"@@main+//:c"},
			"@@main+//:c": {},
			"@@main+//:x": {},
		})

		labels, err := computeQueryExpansion(e, "rdeps(@@main+//..., @@main+//:c)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:a", "@@main+//:b", "@@main+//:c"}, labels)

		labels, err = computeQueryExpansion(e, "rdeps(@@main+//..., @@main+//:c, 1)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:b", "@@main+//:c"}, labels)
	})

	t.Run("Filter", func(t *testing.T) {
		// Canonical labels are matched.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//:*", newQueryExpansionValue("@@main+//:a", "@@main+//:ab", "@@other+//:a")).Times(2)

		labels, err := computeQueryExpansion(e, "filter('^@@main\\+//:a$', @@main+//:*)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:a"}, labels)

		labels, err = computeQueryExpansion(e, "filter('^@@other\\+//', @@main+//:*)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@other+//:a"}, labels)
	})

	t.Run("Kind", func(t *testing.T) {
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//:*", newQueryExpansionValue("@@main+//:a", "@@main+//:b"))
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:a"}.Build()),
		).Return(newSourceFileTargetValue())
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:b"}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
			Definition: model_starlark_pb.Target_Definition_builder{
				RuleTarget: model_starlark_pb.RuleTarget_builder{
					RuleIdentifier: "@@rules_cc+//cc:defs.bzl%cc_library",
				}.Build(),
			}.Build(),
		}.Build()))

		labels, err := computeQueryExpansion(e, "kind('cc_.* rule', @@main+//:*)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:b"}, labels)
	})

	t.Run("AttrDict", func(t *testing.T) {
		// Dict attributes are matched as "{key=value, ...}".
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//:t", newQueryExpansionValue("@@main+//:t")).Times(2)
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:t"}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
			Definition: model_starlark_pb.Target_Definition_builder{
				RuleTarget: model_starlark_pb.RuleTarget_builder{
					RuleIdentifier: "@@rules+//:defs.bzl%my_rule",
					PublicAttrValues: []*model_starlark_pb.RuleTarget_PublicAttrValue{model_starlark_pb.RuleTarget_PublicAttrValue_builder{
						ValueParts: []*model_starlark_pb.Select_Group{
							valueSelectGroup(model_starlark_pb.Value_builder{
								Dict: model_starlark_pb.Dict_builder{
									Entries: []*model_starlark_pb.Dict_Entry{
										model_starlark_pb.Dict_Entry_builder{
											Leaf: model_starlark_pb.Dict_Entry_Leaf_builder{
												Key:   stringValue("A"),
												Value: stringValue("1"),
											}.Build(),
										}.Build(),
										model_starlark_pb.Dict_Entry_builder{
											Leaf: model_starlark_pb.Dict_Entry_Leaf_builder{
												Key:   stringValue("B"),
												Value: stringValue("2"),
											}.Build(),
										}.Build(),
									},
								}.Build(),
							}.Build()),
						},
					}.Build()},
				}.Build(),
			}.Build(),
		}.Build())).Times(2)
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, model_analysis_pb.CompiledBzlFileGlobal_Key_builder{Identifier: "@@rules+//:defs.bzl%my_rule"}.Build()),
		).Return(newRuleDefinitionValue(model_starlark_pb.Rule_Definition_NON_EXECUTABLE, model_starlark_pb.NamedAttr_builder{
			Name: "env",
			Attr: model_starlark_pb.Attr_builder{
				StringDict: &model_starlark_pb.Attr_StringDictType{},
			}.Build(),
		}.Build())).Times(2)

		labels, err := computeQueryExpansion(e, "attr(env, '[{ ]B=2[,}]', @@main+//:t)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:t"}, labels)

		labels, err = computeQueryExpansion(e, "attr(env, 'B: 2', @@main+//:t)")
		require.NoError(t, err)
		require.Empty(t, labels)
	})

	t.Run("AttrTest", func(t *testing.T) {
		// Attributes that are common to all tests should be
		// matchable. shard_count and args are configurable.
		intValue := func(i int64) *model_starlark_pb.Value {
			return model_starlark_pb.Value_builder{
				Int: proto.ValueOrDefaultBytes(model_starlark.EncodeInt(big.NewInt(i))),
			}.Build()
		}
		newTestTarget := func(test *model_starlark_pb.RuleTarget_Test) model_core.Message[*model_analysis_pb.Target_Value, model_core.CreatedObjectTree] {
			test.SetArgs([]*model_starlark_pb.Select_Group{valueSelectGroup(model_starlark_pb.Value_builder{
				List: &model_starlark_pb.List{},
			}.Build())})
			return model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
				Definition: model_starlark_pb.Target_Definition_builder{
					RuleTarget: model_starlark_pb.RuleTarget_builder{
						RuleIdentifier: "@@rules+//:defs.bzl%my_test",
						Test:           test,
					}.Build(),
				}.Build(),
			}.Build())
		}
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//:*", newQueryExpansionValue("@@main+//:a", "@@main+//:b")).AnyTimes()
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:a"}.Build()),
		).Return(newTestTarget(model_starlark_pb.RuleTarget_Test_builder{
			Size:    model_starlark_pb.TestSize_SMALL,
			Timeout: model_starlark_pb.TestTimeout_SHORT,
			ShardCount: []*model_starlark_pb.Select_Group{model_starlark_pb.Select_Group_builder{
				Conditions: []*model_starlark_pb.Select_Condition{model_starlark_pb.Select_Condition_builder{
					ConditionIdentifier: "@@main+//:cond",
					Value:               intValue(4),
				}.Build()},
				NoMatchValue: proto.ValueOrDefault(intValue(2)),
			}.Build()},
		}.Build())).AnyTimes()
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:b"}.Build()),
		).Return(newTestTarget(model_starlark_pb.RuleTarget_Test_builder{
			Size:       model_starlark_pb.TestSize_MEDIUM,
			Timeout:    model_starlark_pb.TestTimeout_MODERATE,
			Flaky:      true,
			ShardCount: []*model_starlark_pb.Select_Group{valueSelectGroup(intValue(1))},
		}.Build())).AnyTimes()
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, model_analysis_pb.CompiledBzlFileGlobal_Key_builder{Identifier: "@@rules+//:defs.bzl%my_test"}.Build()),
		).Return(newRuleDefinitionValue(model_starlark_pb.Rule_Definition_TEST)).AnyTimes()

		for query, expectedLabels := range map[string][]string{
			"attr(size, '^small$', @@main+//:*)":       {"@@main+//:a"},
			"attr(timeout, '^moderate$', @@main+//:*)": {"@@main+//:b"},
			"attr(flaky, '^1$', @@main+//:*)":          {"@@main+//:b"},
			"attr(shard_count, '^4$', @@main+//:*)":    {"@@main+//:a"},
			"attr(shard_count, '^[12]$', @@main+//:*)": {"@@main+//:a", "@@main+//:b"},
			"attr(local, '^1$', @@main+//:*)":          {},
		} {
			labels, err := computeQueryExpansion(e, query)
			require.NoError(t, err, query)
			require.Equal(t, expectedLabels, labels, query)
		}
	})

	t.Run("AttrTooManyValues", func(t *testing.T) {
		// Concatenating select() expressions causes the number
		// of possible values of an attribute to grow
		// exponentially. Instead of only considering some of
		// them, attr() should fail.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//:t", newQueryExpansionValue("@@main+//:t"))
		newSelectGroup := func(prefix string) *model_starlark_pb.Select_Group {
			group := model_starlark_pb.Select_Group_builder{
				NoMatchValue: proto.ValueOrDefault(stringValue(prefix)),
			}.Build()
			for i := range 31 {
				group.SetConditions(append(group.GetConditions(), model_starlark_pb.Select_Condition_builder{
					ConditionIdentifier: fmt.Sprintf("@@main+//:condition_%02d", i),
					Value:               stringValue(fmt.Sprintf("%s%d", prefix, i)),
				}.Build()))
			}
			return group
		}
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:t"}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
			Definition: model_starlark_pb.Target_Definition_builder{
				RuleTarget: model_starlark_pb.RuleTarget_builder{
					RuleIdentifier: "@@rules+//:defs.bzl%my_rule",
					PublicAttrValues: []*model_starlark_pb.RuleTarget_PublicAttrValue{model_starlark_pb.RuleTarget_PublicAttrValue_builder{
						// 32 * 32 = 1024 possible values.
						ValueParts: []*model_starlark_pb.Select_Group{
							newSelectGroup("a"),
							newSelectGroup("b"),
						},
					}.Build()},
				}.Build(),
			}.Build(),
		}.Build()))
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, model_analysis_pb.CompiledBzlFileGlobal_Key_builder{Identifier: "@@rules+//:defs.bzl%my_rule"}.Build()),
		).Return(newRuleDefinitionValue(model_starlark_pb.Rule_Definition_NON_EXECUTABLE, model_starlark_pb.NamedAttr_builder{
			Name: "copts",
			Attr: model_starlark_pb.Attr_builder{
				String: &model_starlark_pb.Attr_StringType{},
			}.Build(),
		}.Build()))

		_, err := computeQueryExpansion(e, "attr(copts, x, @@main+//:t)")
		require.EqualError(t, err, "attr \"copts\" of target \"@@main+//:t\": more than 1000 possible values exist, due to concatenated select() expressions")
	})

	t.Run("Tests", func(t *testing.T) {
		// Only tests and the tests contained in test suites
		// should be yielded. Test suites may refer to tests in
		// other packages, and the results should be
		// deduplicated.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//:*", newQueryExpansionValue("@@main+//:a", "@@main+//:b", "@@main+//:c", "@@main+//:d"))
		expectTarget := func(l string, definition *model_starlark_pb.Target_Definition) {
			e.EXPECT().GetTargetValue(
				testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: l}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
				Definition: definition,
			}.Build()))
		}
		expectTarget("@@main+//:a", model_starlark_pb.Target_Definition_builder{
			RuleTarget: model_starlark_pb.RuleTarget_builder{
				RuleIdentifier: "@@rules+//:defs.bzl%my_test",
				Test:           &model_starlark_pb.RuleTarget_Test{},
			}.Build(),
		}.Build())
		expectTarget("@@main+//:b", ruleTarget("b").GetDefinition())
		expectTarget("@@main+//:c", sourceFileTarget("c").GetDefinition())
		expectTarget("@@main+//:d", model_starlark_pb.Target_Definition_builder{
			TestSuite: model_starlark_pb.TestSuite_builder{
				Tests: newTestSuiteTests("@@main+//:a", "@@other+//:x"),
			}.Build(),
		}.Build())
		e.EXPECT().GetTestSuiteTestsValue(
			testutil.EqProto(t, model_analysis_pb.TestSuiteTests_Key_builder{Label: "@@main+//:d"}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.TestSuiteTests_Value_builder{
			TargetLabels: newTargetLabels("@@main+//:a", "@@other+//:x"),
		}.Build()))

		labels, err := computeQueryExpansion(e, "tests(@@main+//:*)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:a", "@@other+//:x"}, labels)
	})

	t.Run("TestSuiteAttrs", func(t *testing.T) {
		// tag() and attr(tags, ...) should only consider the
		// positive tags of test suites and "manual". Negative
		// tags and sizes are only used to filter tests.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//:suite", newQueryExpansionValue("@@main+//:suite")).AnyTimes()
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:suite"}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
			Definition: model_starlark_pb.Target_Definition_builder{
				TestSuite: model_starlark_pb.TestSuite_builder{
					Tests:        newTestSuiteTests("@@main+//:a"),
					PositiveTags: []string{"integration"},
					NegativeTags: []string{"flaky"},
					Sizes:        []model_starlark_pb.TestSize{model_starlark_pb.TestSize_SMALL},
					Manual:       true,
				}.Build(),
			}.Build(),
		}.Build())).AnyTimes()

		for query, expected := range map[string][]string{
			"tag(integration, @@main+//:suite)":                          {"@@main+//:suite"},
			"tag(manual, @@main+//:suite)":                               {"@@main+//:suite"},
			"tag(flaky, @@main+//:suite)":                                {},
			"tag(small, @@main+//:suite)":                                {},
			"attr(tags, \"\\[integration, manual\\]\", @@main+//:suite)": {"@@main+//:suite"},
			"attr(tests, \"\\[@@main\\+//:a\\]\", @@main+//:suite)":      {"@@main+//:suite"},
			"kind(\"test_suite rule\", @@main+//:suite)":                 {"@@main+//:suite"},
		} {
			labels, err := computeQueryExpansion(e, query)
			require.NoError(t, err, query)
			require.Equal(t, expected, labels, query)
		}
	})

	t.Run("Visible", func(t *testing.T) {
		// visibleTo returns a package group containing a set of
		// packages in the main repo.
		visibleTo := func(packages ...string) *model_starlark_pb.PackageGroup {
			var overrides []*model_starlark_pb.PackageGroup_Package
			for _, p := range packages {
				overrides = append(overrides, model_starlark_pb.PackageGroup_Package_builder{
					Component:      p,
					IncludePackage: true,
					Subpackages:    &model_starlark_pb.PackageGroup_Subpackages{},
				}.Build())
			}
			return model_starlark_pb.PackageGroup_builder{
				Tree: model_starlark_pb.PackageGroup_Subpackages_builder{
					OverridesInline: model_starlark_pb.PackageGroup_Subpackages_Overrides_builder{
						Packages: []*model_starlark_pb.PackageGroup_Package{model_starlark_pb.PackageGroup_Package_builder{
							Component: "main+",
							Subpackages: model_starlark_pb.PackageGroup_Subpackages_builder{
								OverridesInline: model_starlark_pb.PackageGroup_Subpackages_Overrides_builder{
									Packages: overrides,
								}.Build(),
							}.Build(),
						}.Build()},
					}.Build(),
				}.Build(),
			}.Build()
		}
		ruleTargetVisibleTo := func(packages ...string) *model_starlark_pb.Target_Definition {
			return model_starlark_pb.Target_Definition_builder{
				RuleTarget: model_starlark_pb.RuleTarget_builder{
					RuleIdentifier:   "@@rules+//:defs.bzl%my_rule",
					InheritableAttrs: model_starlark_pb.InheritableAttrs_builder{Visibility: visibleTo(packages...)}.Build(),
				}.Build(),
			}.Build()
		}
		// All targets in the input are part of the root package.
		definitions := map[string]*model_starlark_pb.Target_Definition{
			"@@main+//:p":      ruleTargetVisibleTo("p"),
			"@@main+//:pq":     ruleTargetVisibleTo("p", "q"),
			"@@main+//:none":   ruleTargetVisibleTo(),
			"@@main+//:source": model_starlark_pb.Target_Definition_builder{SourceFileTarget: model_starlark_pb.SourceFileTarget_builder{Visibility: visibleTo("q")}.Build()}.Build(),
			"@@main+//:group":  model_starlark_pb.Target_Definition_builder{PackageGroup: proto.ValueOrDefault(visibleTo())}.Build(),
			"@@main+//:output": model_starlark_pb.Target_Definition_builder{PredeclaredOutputFileTarget: model_starlark_pb.PredeclaredOutputFileTarget_builder{OwnerTargetName: "p"}.Build()}.Build(),
			// Aliases should not be followed, meaning only
			// the visibility of the alias itself is considered.
			"@@main+//:alias": model_starlark_pb.Target_Definition_builder{Alias: model_starlark_pb.Alias_builder{Visibility: visibleTo("q")}.Build()}.Build(),
		}
		inputLabels := slices.Sorted(maps.Keys(definitions))
		computeVisible := func(t *testing.T, predicate string, predicatePackages ...string) []string {
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectQueryExpansion(e, normalizeCanonicalQuery("packages("+predicate+")"), newQueryExpansionValue(predicatePackages...))
			expectQueryExpansion(e, "@@main+//:*", newQueryExpansionValue(inputLabels...))
			for l, definition := range definitions {
				e.EXPECT().GetTargetValue(
					testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: l}.Build()),
				).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
					Definition: definition,
				}.Build())).AnyTimes()
			}
			labels, err := computeQueryExpansion(e, "visible("+predicate+", @@main+//:*)")
			require.NoError(t, err)
			return labels
		}

		t.Run("SinglePackage", func(t *testing.T) {
			require.Equal(t, []string{
				"@@main+//:group",
				"@@main+//:output",
				"@@main+//:p",
				"@@main+//:pq",
			}, computeVisible(t, "@@main+//p:x", "@@main+//p"))
		})

		t.Run("AllPackages", func(t *testing.T) {
			// Targets need to be visible to all packages in
			// the predicate.
			require.Equal(t, []string{
				"@@main+//:group",
				"@@main+//:pq",
			}, computeVisible(t, "set(@@main+//p:x @@main+//q:x)", "@@main+//p", "@@main+//q"))
		})

		t.Run("SamePackage", func(t *testing.T) {
			// Targets are always visible within the same
			// package.
			require.Equal(t, inputLabels, computeVisible(t, "@@main+//:x", "@@main+"))
		})

		t.Run("EmptyPredicate", func(t *testing.T) {
			require.Equal(t, inputLabels, computeVisible(t, "@@main+//p:*"))
		})
	})

	t.Run("Executables", func(t *testing.T) {
		// Only targets whose rule is executable should be
		// yielded. Tests are executable as well, but should only
		// be yielded by tests().
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//:*", newQueryExpansionValue("@@main+//:a", "@@main+//:b", "@@main+//:c", "@@main+//:d"))
		expectTarget := func(l, ruleIdentifier string) {
			definition := sourceFileTarget("").GetDefinition()
			if ruleIdentifier != "" {
				definition = model_starlark_pb.Target_Definition_builder{
					RuleTarget: model_starlark_pb.RuleTarget_builder{RuleIdentifier: ruleIdentifier}.Build(),
				}.Build()
			}
			e.EXPECT().GetTargetValue(
				testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: l}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
				Definition: definition,
			}.Build()))
		}
		expectRuleDefinition := func(ruleIdentifier string, definition *model_starlark_pb.Rule_Definition) {
			e.EXPECT().GetCompiledBzlFileGlobalValue(
				testutil.EqProto(t, model_analysis_pb.CompiledBzlFileGlobal_Key_builder{Identifier: ruleIdentifier}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.CompiledBzlFileGlobal_Value_builder{
				Global: model_starlark_pb.Value_builder{
					Rule: model_starlark_pb.Rule_builder{
						Definition: proto.ValueOrDefault(definition),
					}.Build(),
				}.Build(),
			}.Build()))
		}
		expectTarget("@@main+//:a", "@@rules+//:defs.bzl%my_binary")
		expectTarget("@@main+//:b", "@@rules+//:defs.bzl%my_test")
		expectTarget("@@main+//:c", "@@rules+//:defs.bzl%my_library")
		expectTarget("@@main+//:d", "")
		expectRuleDefinition("@@rules+//:defs.bzl%my_binary", model_starlark_pb.Rule_Definition_builder{Type: model_starlark_pb.Rule_Definition_EXECUTABLE}.Build())
		expectRuleDefinition("@@rules+//:defs.bzl%my_test", model_starlark_pb.Rule_Definition_builder{Type: model_starlark_pb.Rule_Definition_TEST}.Build())
		expectRuleDefinition("@@rules+//:defs.bzl%my_library", model_starlark_pb.Rule_Definition_builder{Type: model_starlark_pb.Rule_Definition_NON_EXECUTABLE}.Build())

		labels, err := computeQueryExpansion(e, "executables(@@main+//:*)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:a"}, labels)
	})

	t.Run("Tag", func(t *testing.T) {
		// Tags are matched exactly. Targets other than rule
		// targets don't have any tags, and should not match.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//:*", newQueryExpansionValue("@@main+//:a", "@@main+//:b", "@@main+//:c")).Times(2)
		expectTarget := func(l string, definition *model_starlark_pb.Target_Definition) {
			e.EXPECT().GetTargetValue(
				testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: l}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
				Definition: definition,
			}.Build())).Times(2)
		}
		expectTarget("@@main+//:a", ruleTarget("a", "manual", "x").GetDefinition())
		expectTarget("@@main+//:b", ruleTarget("b", "manually").GetDefinition())
		expectTarget("@@main+//:c", sourceFileTarget("c").GetDefinition())

		labels, err := computeQueryExpansion(e, "tag(manual, @@main+//:*)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:a"}, labels)

		labels, err = computeQueryExpansion(e, "tag(y, @@main+//:*)")
		require.NoError(t, err)
		require.Empty(t, labels)
	})

	t.Run("Packages", func(t *testing.T) {
		// Each package should only be returned once. The root
		// package of a repo is identified by the repo's label.
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, normalizeCanonicalQuery("set(@@main+//a:b @@main+//a:c @@main+//:d @@other+//:e)"), newQueryExpansionValue("@@main+//:d", "@@main+//a:b", "@@main+//a:c", "@@other+//:e"))

		labels, err := computeQueryExpansion(e, "packages(set(@@main+//a:b @@main+//a:c @@main+//:d @@other+//:e))")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+", "@@main+//a", "@@other+"}, labels)
	})

	t.Run("Streaming", func(t *testing.T) {
		// Expressions whose results are sorted if the results of
		// their operands are sorted should be evaluated without
		// loading the results of their operands into memory.
		t.Run("SomeStopsEarly", func(t *testing.T) {
			// some() should stop reading its operand once
			// enough labels have been obtained.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectTargetLabels(e, "deps(@@main+//:x)", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
				return []*model_analysis_pb.TargetLabel{
					leafTargetLabel("@@main+//:a"),
					leafTargetLabel("@@main+//:b"),
					newUnloadableTargetLabelsParent(patcher, "@@main+//:c", "@@main+//:d"),
				}
			})

			labels, _ := computeSetOperation(t, e, "some(deps(@@main+//:x), 2)")
			require.Equal(t, []string{"@@main+//:a", "@@main+//:b"}, labels)
		})

		t.Run("InvalidOperand", func(t *testing.T) {
			// Invalid labels contained in the operand should
			// cause evaluation to fail, as opposed to the
			// operand being truncated.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectTargetLabels(e, "deps(@@main+//:x)", func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
				return []*model_analysis_pb.TargetLabel{
					leafTargetLabel("@@main+//:a"),
					newUnloadableTargetLabelsParent(patcher, "@@main+//:c", "@@main+//:d"),
				}
			})

			_, err := computeQueryExpansion(e, "filter(., deps(@@main+//:x))")
			require.ErrorContains(t, err, "invalid target label \"invalid @@main+//:c\"")
		})

		// Operand spanning multiple batches, where packages
		// cross batch boundaries.
		var operandLabels []string
		for i := range 2500 {
			operandLabels = append(operandLabels, fmt.Sprintf("@@main+//p%d:t%04d", i/1200, i))
		}

		t.Run("FilterAcrossBatches", func(t *testing.T) {
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectQueryExpansion(e, "deps(@@main+//:x)", newQueryExpansionValue(operandLabels...))

			labels, _ := computeSetOperation(t, e, "filter('[05]$', deps(@@main+//:x))")
			var expectedLabels []string
			for _, l := range operandLabels {
				if strings.HasSuffix(l, "0") || strings.HasSuffix(l, "5") {
					expectedLabels = append(expectedLabels, l)
				}
			}
			require.Equal(t, expectedLabels, labels)
		})

		t.Run("PackagesAcrossBatches", func(t *testing.T) {
			// Packages spanning multiple batches should only
			// be yielded once.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectQueryExpansion(e, "deps(@@main+//:x)", newQueryExpansionValue(operandLabels...))

			labels, _ := computeSetOperation(t, e, "packages(deps(@@main+//:x))")
			require.Equal(t, []string{"@@main+//p0", "@@main+//p1", "@@main+//p2"}, labels)
		})

		t.Run("MissingDependenciesAcrossBatches", func(t *testing.T) {
			// If targets in one batch are not available yet,
			// targets in other batches should still be
			// requested.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectQueryExpansion(e, "deps(@@main+//:x)", newQueryExpansionValue(operandLabels...))
			requested := map[string]bool{}
			e.EXPECT().GetTargetValue(gomock.Any()).
				DoAndReturn(func(key *model_analysis_pb.Target_Key) model_core.Message[*model_analysis_pb.Target_Value, model_core.CreatedObjectTree] {
					requested[key.GetLabel()] = true
					if key.GetLabel() == operandLabels[0] {
						return model_core.Message[*model_analysis_pb.Target_Value, model_core.CreatedObjectTree]{}
					}
					return newSourceFileTargetValue()
				}).
				AnyTimes()

			_, err := computeQueryExpansion(e, "rules(deps(@@main+//:x))")
			require.ErrorIs(t, err, evaluation.ErrMissingDependency)
			require.Len(t, requested, len(operandLabels))
		})

		t.Run("Siblings", func(t *testing.T) {
			// Each package should only be loaded once, even
			// if the operand contains multiple targets in it.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectQueryExpansion(e, "deps(@@main+//:x)", newQueryExpansionValue("@@main+//a:x", "@@main+//a:y", "@@main+//b:z"))
			for canonicalPackage, targets := range map[string][]*model_starlark_pb.Target{
				"@@main+//a": {ruleTarget("x"), ruleTarget("y"), sourceFileTarget("z")},
				"@@main+//b": {sourceFileTarget("w"), ruleTarget("z")},
			} {
				value := &model_analysis_pb.PackageTargets_Value{}
				for _, target := range targets {
					value.SetTargets(append(value.GetTargets(), model_analysis_pb.Package_Value_Target_builder{
						Leaf: proto.ValueOrDefault(target),
					}.Build()))
				}
				e.EXPECT().GetPackageTargetsValue(
					testutil.EqProto(t, model_analysis_pb.PackageTargets_Key_builder{Package: canonicalPackage}.Build()),
				).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](value))
			}

			labels, _ := computeSetOperation(t, e, "siblings(deps(@@main+//:x))")
			require.Equal(t, []string{"@@main+//a:x", "@@main+//a:y", "@@main+//a:z", "@@main+//b:w", "@@main+//b:z"}, labels)
		})

		t.Run("SiblingsOfWildcardsSorted", func(t *testing.T) {
			// Sets of target patterns are sorted as strings,
			// which differs from the order of packages.
			// Packages should be visited in label order.
			e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			expectCaptureObjects(e)
			expectPackage(e, "@@main+//a", ruleTarget("x"))
			expectPackage(e, "@@main+//a/c", ruleTarget("y"))
			expectPackage(e, "@@main+//b", ruleTarget("z"))

			labels, _ := computeSetOperation(t, e, "siblings(set(@@main+//b:* @@main+//a/c:* @@main+//a:*))")
			require.Equal(t, []string{"@@main+//a:x", "@@main+//a/c:y", "@@main+//b:z"}, labels)
		})
	})

	t.Run("Some", func(t *testing.T) {
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//...", newQueryExpansionValue("@@main+//:a", "@@main+//:b", "@@main+//:c"))

		labels, err := computeQueryExpansion(e, "some(@@main+//..., 2)")
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:a", "@@main+//:b"}, labels)
	})

	t.Run("SomeEmpty", func(t *testing.T) {
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectQueryExpansion(e, "@@main+//...", newQueryExpansionValue())

		_, err := computeQueryExpansion(e, "some(@@main+//...)")
		require.EqualError(t, err, "argument set of some() is empty")
	})

	t.Run("Unsupported", func(t *testing.T) {
		e := NewMockQueryExpansionEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		_, err := computeQueryExpansion(e, "somepath(@@main+//:a, @@main+//:b)")
		require.EqualError(t, err, "somepath() is not supported yet")
	})
}

// newStringValue creates a Value message containing a string that is
// stored inline.
func newStringValue(s string) *model_starlark_pb.Value {
	contents := &model_filesystem_pb.MaybeInlineFileContents{}
	if s != "" {
		contents.SetContentsInline([]byte(s))
	}
	return model_starlark_pb.Value_builder{
		Str: proto.ValueOrDefault(contents),
	}.Build()
}
