package starlark_test

import (
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"go.uber.org/mock/gomock"
)

var allTestSizes = []model_starlark_pb.TestSize{
	model_starlark_pb.TestSize_SMALL,
	model_starlark_pb.TestSize_MEDIUM,
	model_starlark_pb.TestSize_LARGE,
	model_starlark_pb.TestSize_ENORMOUS,
}

func TestTestSuite(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	referenceFormat := util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1))
	encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
	bzlFileBuiltins, _ := model_starlark.GetBuiltins[object.LocalReference, model_core.CreatedObjectTree]()
	testSuiteBuiltin := util.Must(bzlFileBuiltins["native"].(starlark.HasAttrs).Attr(&starlark.Thread{}, "test_suite"))
	labelResolver := NewMockLabelResolver(ctrl)

	// newPackage creates a Starlark thread for evaluating a BUILD
	// file in package @@main+//foo.
	newPackage := func(t *testing.T) (*starlark.Thread, *model_starlark.TargetRegistrar[object.LocalReference, model_core.CreatedObjectTree]) {
		objectManager := NewMockObjectManagerForTesting(ctrl)
		targetRegistrar := model_starlark.NewTargetRegistrar[object.LocalReference, model_core.CreatedObjectTree](
			ctx,
			encoder,
			&inlinedtree.Options{
				ReferenceFormat:  referenceFormat,
				MaximumSizeBytes: 32 * 1024,
			},
			objectManager,
			model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.InheritableAttrs{}),
		)
		t.Cleanup(targetRegistrar.Discard)

		thread := &starlark.Thread{}
		thread.SetLocal(model_starlark.TargetRegistrarKey, targetRegistrar)
		thread.SetLocal(model_starlark.CanonicalPackageKey, util.Must(label.NewCanonicalPackage("@@main+//foo")))
		thread.SetLocal(model_starlark.LabelResolverKey, labelResolver)
		thread.SetLocal(model_starlark.ValueEncodingOptionsKey, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{
			Context:                ctx,
			ObjectEncoder:          encoder,
			ObjectReferenceFormat:  referenceFormat,
			ObjectCapturer:         objectManager,
			ObjectMinimumSizeBytes: 32 * 1024,
			ObjectMaximumSizeBytes: 128 * 1024,
		})
		return thread, targetRegistrar
	}
	// finalize fills in the tests of test suites that don't list
	// any tests explicitly.
	finalize := func(thread *starlark.Thread, targetRegistrar *model_starlark.TargetRegistrar[object.LocalReference, model_core.CreatedObjectTree]) error {
		return targetRegistrar.Finalize(
			thread.Local(model_starlark.CanonicalPackageKey).(label.CanonicalPackage),
			thread.Local(model_starlark.ValueEncodingOptionsKey).(*model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]),
		)
	}
	// getTestSuite extracts a test suite from a package after all
	// targets have been declared.
	getTestSuite := func(t *testing.T, targetRegistrar *model_starlark.TargetRegistrar[object.LocalReference, model_core.CreatedObjectTree], name string) *model_starlark_pb.TestSuite {
		testSuite := targetRegistrar.GetAndRemoveTarget(name).Message.GetTestSuite()
		require.NotNil(t, testSuite)
		testSuite.Visibility = nil
		return testSuite
	}
	// callTestSuite invokes test_suite() from within a BUILD file
	// in package @@main+//foo, returning the test suite that is
	// registered.
	callTestSuite := func(t *testing.T, kwargs ...starlark.Tuple) (*model_starlark_pb.TestSuite, error) {
		thread, targetRegistrar := newPackage(t)
		if _, err := starlark.Call(thread, testSuiteBuiltin, nil, append([]starlark.Tuple{{starlark.String("name"), starlark.String("suite")}}, kwargs...)); err != nil {
			return nil, err
		}
		require.NoError(t, finalize(thread, targetRegistrar))
		return getTestSuite(t, targetRegistrar, "suite"), nil
	}
	stringList := func(values ...string) starlark.Value {
		l := make([]starlark.Value, 0, len(values))
		for _, v := range values {
			l = append(l, starlark.String(v))
		}
		return starlark.NewList(l)
	}

	t.Run("Defaults", func(t *testing.T) {
		// Without any tags, tests of all sizes are included.
		testSuite, err := callTestSuite(t)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark_pb.TestSuite{
			Sizes: allTestSizes,
		}, testSuite)
		require.Empty(t, model_starlark.GetTestSuiteTags(testSuite))
		require.Empty(t, model_starlark.GetTestSuiteQueryTags(testSuite))
	})

	t.Run("Tests", func(t *testing.T) {
		// Labels of tests should be resolved, sorted and
		// deduplicated.
		testSuite, err := callTestSuite(t, starlark.Tuple{
			starlark.String("tests"),
			stringList("c", "//bar:b", ":a", "@@main+//foo:a"),
		})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark_pb.TestSuite{
			Tests: []*model_starlark_pb.TestSuite_Test{
				{Level: &model_starlark_pb.TestSuite_Test_Leaf{Leaf: "@@main+//bar:b"}},
				{Level: &model_starlark_pb.TestSuite_Test_Leaf{Leaf: "@@main+//foo:a"}},
				{Level: &model_starlark_pb.TestSuite_Test_Leaf{Leaf: "@@main+//foo:c"}},
			},
			Sizes: allTestSizes,
		}, testSuite)
	})

	t.Run("UnknownRepo", func(t *testing.T) {
		// Test suites may refer to tests in repos that are not
		// visible from the current module (e.g., repos that are
		// dev dependencies of a module that is not the root
		// module). This should not cause test_suite() to fail.
		labelResolver.EXPECT().GetCanonicalRepo(util.Must(label.NewCanonicalRepo("main+")), util.Must(label.NewApparentRepo("dev"))).Return(nil, nil)

		testSuite, err := callTestSuite(t, starlark.Tuple{starlark.String("tests"), stringList("@dev//:t")})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark_pb.TestSuite{
			Tests: []*model_starlark_pb.TestSuite_Test{
				{Level: &model_starlark_pb.TestSuite_Test_Leaf{Leaf: "@@[unknown repo 'dev' requested from main+]//:t"}},
			},
			Sizes: allTestSizes,
		}, testSuite)
	})

	t.Run("ImplicitTests", func(t *testing.T) {
		// Test suites that don't list any tests explicitly
		// should contain all tests in the same package that are
		// not tagged "manual" and pass the test suite's filters.
		// This should also apply to tests that are declared
		// after the test suite.
		thread, targetRegistrar := newPackage(t)
		newRule := func(ruleType model_starlark_pb.Rule_Definition_Type) starlark.Value {
			identifier := util.Must(label.NewCanonicalStarlarkIdentifier("@@rules+//:defs.bzl%my_rule"))
			return model_starlark.NewRule(
				&identifier,
				model_starlark.NewStarlarkRuleDefinition[object.LocalReference, model_core.CreatedObjectTree](
					/* attrs = */ nil,
					/* buildSetting = */ nil,
					/* cfg = */ nil,
					/* execGroups = */ nil,
					/* implementation = */ model_starlark.NamedFunction[object.LocalReference, model_core.CreatedObjectTree]{},
					/* initializer = */ nil,
					/* provides = */ nil,
					ruleType,
					/* subrules = */ nil,
				),
			)
		}
		myTest := newRule(model_starlark_pb.Rule_Definition_TEST)
		myLibrary := newRule(model_starlark_pb.Rule_Definition_NON_EXECUTABLE)
		call := func(callee starlark.Value, kwargs ...starlark.Tuple) {
			_, err := starlark.Call(thread, callee, nil, kwargs)
			require.NoError(t, err)
		}
		name := func(name string) starlark.Tuple {
			return starlark.Tuple{starlark.String("name"), starlark.String(name)}
		}

		call(myTest, name("a_small"), starlark.Tuple{starlark.String("size"), starlark.String("small")})
		call(testSuiteBuiltin, name("all"))
		call(testSuiteBuiltin, name("small_without_flaky"), starlark.Tuple{starlark.String("tags"), stringList("small", "-flaky")})
		call(myTest, name("b_large"), starlark.Tuple{starlark.String("size"), starlark.String("large")})
		call(myTest, name("c_flaky"), starlark.Tuple{starlark.String("tags"), stringList("flaky")})
		call(myTest, name("d_manual"), starlark.Tuple{starlark.String("tags"), stringList("manual")})
		call(myLibrary, name("e_library"))
		call(testSuiteBuiltin, name("explicit"), starlark.Tuple{starlark.String("tests"), stringList(":d_manual")})
		require.NoError(t, finalize(thread, targetRegistrar))

		testutil.RequireEqualProto(t, &model_starlark_pb.TestSuite{
			Tests: []*model_starlark_pb.TestSuite_Test{
				{Level: &model_starlark_pb.TestSuite_Test_Leaf{Leaf: "@@main+//foo:a_small"}},
				{Level: &model_starlark_pb.TestSuite_Test_Leaf{Leaf: "@@main+//foo:b_large"}},
				{Level: &model_starlark_pb.TestSuite_Test_Leaf{Leaf: "@@main+//foo:c_flaky"}},
			},
			Sizes: allTestSizes,
		}, getTestSuite(t, targetRegistrar, "all"))
		testutil.RequireEqualProto(t, &model_starlark_pb.TestSuite{
			Tests: []*model_starlark_pb.TestSuite_Test{
				{Level: &model_starlark_pb.TestSuite_Test_Leaf{Leaf: "@@main+//foo:a_small"}},
			},
			NegativeTags: []string{"flaky"},
			Sizes:        []model_starlark_pb.TestSize{model_starlark_pb.TestSize_SMALL},
		}, getTestSuite(t, targetRegistrar, "small_without_flaky"))

		// Test suites listing tests explicitly should not be
		// affected.
		testutil.RequireEqualProto(t, &model_starlark_pb.TestSuite{
			Tests: []*model_starlark_pb.TestSuite_Test{
				{Level: &model_starlark_pb.TestSuite_Test_Leaf{Leaf: "@@main+//foo:d_manual"}},
			},
			Sizes: allTestSizes,
		}, getTestSuite(t, targetRegistrar, "explicit"))
	})

	t.Run("Tags", func(t *testing.T) {
		// Tags should be split up into positive and negative
		// tags, test sizes, and the "manual" tag. Prefixing a
		// tag with "+" should allow filtering on tags that
		// would otherwise have a special meaning.
		testSuite, err := callTestSuite(t, starlark.Tuple{
			starlark.String("tags"),
			stringList("small", "-flaky", "manual", "+manual", "+small", "requires-network", "-medium", "-nightly", "requires-network"),
		})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark_pb.TestSuite{
			PositiveTags: []string{"manual", "requires-network"},
			NegativeTags: []string{"flaky", "nightly"},
			Sizes:        []model_starlark_pb.TestSize{model_starlark_pb.TestSize_SMALL},
			Manual:       true,
		}, testSuite)

		// The tags should be reconstructed in a form that
		// yields the same test suite if provided to
		// test_suite().
		tags := model_starlark.GetTestSuiteTags(testSuite)
		require.Equal(t, []string{"+manual", "-flaky", "-nightly", "manual", "requires-network", "small"}, tags)
		reconstructedTestSuite, err := callTestSuite(t, starlark.Tuple{starlark.String("tags"), stringList(tags...)})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, testSuite, reconstructedTestSuite)

		// tag() and attr(tags, ...) only match the positive
		// tags and "manual".
		require.Equal(t, []string{"manual", "requires-network"}, model_starlark.GetTestSuiteQueryTags(testSuite))
	})

	t.Run("NegativeSizesOnly", func(t *testing.T) {
		// Only excluding sizes should cause all other sizes to
		// be included.
		testSuite, err := callTestSuite(t, starlark.Tuple{starlark.String("tags"), stringList("-enormous", "-small")})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark_pb.TestSuite{
			Sizes: []model_starlark_pb.TestSize{
				model_starlark_pb.TestSize_MEDIUM,
				model_starlark_pb.TestSize_LARGE,
			},
		}, testSuite)
		tags := model_starlark.GetTestSuiteTags(testSuite)
		require.Equal(t, []string{"-enormous", "-small"}, tags)
		reconstructedTestSuite, err := callTestSuite(t, starlark.Tuple{starlark.String("tags"), stringList(tags...)})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, testSuite, reconstructedTestSuite)
	})

	t.Run("SingleSizeRemaining", func(t *testing.T) {
		// Excluding all sizes but one should be equivalent to
		// selecting that size.
		testSuite, err := callTestSuite(t, starlark.Tuple{starlark.String("tags"), stringList("-enormous", "-large", "-small")})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark_pb.TestSuite{
			Sizes: []model_starlark_pb.TestSize{model_starlark_pb.TestSize_MEDIUM},
		}, testSuite)
		require.Equal(t, []string{"medium"}, model_starlark.GetTestSuiteTags(testSuite))
	})

	t.Run("ManualOnly", func(t *testing.T) {
		testSuite, err := callTestSuite(t, starlark.Tuple{starlark.String("tags"), stringList("manual")})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark_pb.TestSuite{
			Sizes:  allTestSizes,
			Manual: true,
		}, testSuite)
		require.Equal(t, []string{"manual"}, model_starlark.GetTestSuiteTags(testSuite))
		require.Equal(t, []string{"manual"}, model_starlark.GetTestSuiteQueryTags(testSuite))
	})

	t.Run("OverlappingTags", func(t *testing.T) {
		_, err := callTestSuite(t, starlark.Tuple{starlark.String("tags"), stringList("foo", "-foo")})
		require.EqualError(t, err, "attribute \"tags\": tag \"foo\" is both positive and negative")
	})

	t.Run("NoEligibleTestSizes", func(t *testing.T) {
		// Like Bazel, test sizes are treated as tags of tests.
		// As tests only have a single size, providing multiple
		// positive sizes would cause no tests to be included.
		for _, tags := range [][]string{
			{"small", "-small"},
			{"small", "large"},
			{"small", "+medium"},
			{"-small", "-medium", "-large", "-enormous"},
		} {
			_, err := callTestSuite(t, starlark.Tuple{starlark.String("tags"), stringList(tags...)})
			require.EqualError(t, err, "attribute \"tags\": no eligible test sizes", tags)
		}
	})
}

func TestTestSuiteMatchesTest(t *testing.T) {
	testSuite := &model_starlark_pb.TestSuite{
		PositiveTags: []string{"a", "b"},
		NegativeTags: []string{"c"},
		Sizes: []model_starlark_pb.TestSize{
			model_starlark_pb.TestSize_SMALL,
			model_starlark_pb.TestSize_LARGE,
		},
	}

	t.Run("Match", func(t *testing.T) {
		require.True(t, model_starlark.TestSuiteMatchesTest(testSuite, []string{"a", "b", "d"}, model_starlark_pb.TestSize_SMALL))
		require.True(t, model_starlark.TestSuiteMatchesTest(testSuite, []string{"a", "b"}, model_starlark_pb.TestSize_LARGE))
	})

	t.Run("MissingPositiveTag", func(t *testing.T) {
		require.False(t, model_starlark.TestSuiteMatchesTest(testSuite, []string{"a", "d"}, model_starlark_pb.TestSize_SMALL))
	})

	t.Run("HasNegativeTag", func(t *testing.T) {
		require.False(t, model_starlark.TestSuiteMatchesTest(testSuite, []string{"a", "b", "c"}, model_starlark_pb.TestSize_SMALL))
	})

	t.Run("WrongSize", func(t *testing.T) {
		require.False(t, model_starlark.TestSuiteMatchesTest(testSuite, []string{"a", "b"}, model_starlark_pb.TestSize_MEDIUM))
	})
}
