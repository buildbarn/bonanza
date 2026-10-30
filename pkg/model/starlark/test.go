package starlark

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"
)

// ParseTestSize converts the value of the "size" attribute of a test
// to its enumeration value (e.g., "small" to SMALL).
func ParseTestSize(s string) (model_starlark_pb.TestSize, error) {
	if size, ok := model_starlark_pb.TestSize_value[strings.ToUpper(s)]; ok && s == strings.ToLower(s) {
		return model_starlark_pb.TestSize(size), nil
	}
	return 0, fmt.Errorf("invalid test size %#v", s)
}

// ParseTestTimeout converts the value of the "timeout" attribute of a
// test to its enumeration value (e.g., "short" to SHORT).
func ParseTestTimeout(s string) (model_starlark_pb.TestTimeout, error) {
	if timeout, ok := model_starlark_pb.TestTimeout_value[strings.ToUpper(s)]; ok && s == strings.ToLower(s) {
		return model_starlark_pb.TestTimeout(timeout), nil
	}
	return 0, fmt.Errorf("invalid test timeout %#v", s)
}

// FormatTestSize converts a test size to the value of the "size"
// attribute of a test (e.g., SMALL to "small").
func FormatTestSize(size model_starlark_pb.TestSize) string {
	return strings.ToLower(size.String())
}

// FormatTestTimeout converts a test timeout to the value of the
// "timeout" attribute of a test (e.g., SHORT to "short").
func FormatTestTimeout(timeout model_starlark_pb.TestTimeout) string {
	return strings.ToLower(timeout.String())
}

// GetDefaultTestTimeout returns the timeout of a test whose "timeout"
// attribute is not set, which is determined by its size.
func GetDefaultTestTimeout(size model_starlark_pb.TestSize) model_starlark_pb.TestTimeout {
	switch size {
	case model_starlark_pb.TestSize_SMALL:
		return model_starlark_pb.TestTimeout_SHORT
	case model_starlark_pb.TestSize_MEDIUM:
		return model_starlark_pb.TestTimeout_MODERATE
	case model_starlark_pb.TestSize_LARGE:
		return model_starlark_pb.TestTimeout_LONG
	case model_starlark_pb.TestSize_ENORMOUS:
		return model_starlark_pb.TestTimeout_ETERNAL
	default:
		panic("unknown test size")
	}
}

// newRuleTargetTest constructs the message containing the values of
// attributes that are specific to tests, validating them in the
// process. If no timeout is provided, the timeout corresponding to the
// size of the test is used.
func newRuleTargetTest(size, timeout string, flaky, local bool, shardCount, args []*model_starlark_pb.Select_Group) (*model_starlark_pb.RuleTarget_Test, error) {
	parsedSize, err := ParseTestSize(size)
	if err != nil {
		return nil, fmt.Errorf("attribute \"size\": %w", err)
	}
	parsedTimeout := GetDefaultTestTimeout(parsedSize)
	if timeout != "" {
		parsedTimeout, err = ParseTestTimeout(timeout)
		if err != nil {
			return nil, fmt.Errorf("attribute \"timeout\": %w", err)
		}
	}
	return &model_starlark_pb.RuleTarget_Test{
		Size:       parsedSize,
		Timeout:    parsedTimeout,
		Flaky:      flaky,
		Local:      local,
		ShardCount: shardCount,
		Args:       args,
	}, nil
}

// allTestSizes contains all test sizes, in sorted order.
var allTestSizes = []model_starlark_pb.TestSize{
	model_starlark_pb.TestSize_SMALL,
	model_starlark_pb.TestSize_MEDIUM,
	model_starlark_pb.TestSize_LARGE,
	model_starlark_pb.TestSize_ENORMOUS,
}

// newTestSuite constructs the message containing the definition of a
// test suite, without its list of tests. Tags of the test suite are
// split up into positive and negative tags, test sizes, and the
// "manual" tag.
//
// Like Bazel, tags prefixed with "-" are negative, while tags prefixed
// with "+" are positive. The latter allows tests to be filtered by
// tags that would otherwise have a special meaning (e.g., "+manual").
func newTestSuite(tags []string) (*model_starlark_pb.TestSuite, error) {
	testSuite := &model_starlark_pb.TestSuite{}

	// Like Bazel, the size of a test is treated as one of its tags.
	// Start with all sizes, and remove the ones that don't match
	// each of the tags corresponding to test sizes. As tests only
	// have a single size, providing multiple positive sizes causes
	// no sizes to remain.
	sizes := slices.Clone(allTestSizes)
	filterSizes := func(size model_starlark_pb.TestSize, isPositive bool) {
		newSizes := sizes[:0]
		for _, s := range sizes {
			if (s == size) == isPositive {
				newSizes = append(newSizes, s)
			}
		}
		sizes = newSizes
	}
	for _, tag := range tags {
		if negativeTag, ok := strings.CutPrefix(tag, "-"); ok {
			if size, err := ParseTestSize(negativeTag); err == nil {
				filterSizes(size, false)
			} else {
				testSuite.NegativeTags = append(testSuite.NegativeTags, negativeTag)
			}
		} else if positiveTag, ok := strings.CutPrefix(tag, "+"); ok {
			if size, err := ParseTestSize(positiveTag); err == nil {
				filterSizes(size, true)
			} else {
				testSuite.PositiveTags = append(testSuite.PositiveTags, positiveTag)
			}
		} else if tag == "manual" {
			testSuite.Manual = true
		} else if size, err := ParseTestSize(tag); err == nil {
			filterSizes(size, true)
		} else {
			testSuite.PositiveTags = append(testSuite.PositiveTags, tag)
		}
	}

	slices.Sort(testSuite.PositiveTags)
	testSuite.PositiveTags = slices.Compact(testSuite.PositiveTags)
	slices.Sort(testSuite.NegativeTags)
	testSuite.NegativeTags = slices.Compact(testSuite.NegativeTags)
	for _, tag := range testSuite.NegativeTags {
		if _, found := slices.BinarySearch(testSuite.PositiveTags, tag); found {
			return nil, fmt.Errorf("attribute \"tags\": tag %#v is both positive and negative", tag)
		}
	}

	// Reject test suites for which no sizes are eligible, as these
	// are most likely the result of a mistake.
	if len(sizes) == 0 {
		return nil, errors.New("attribute \"tags\": no eligible test sizes")
	}
	testSuite.Sizes = sizes

	return testSuite, nil
}

// isSpecialTestSuiteTag returns true if a tag of a test suite would not
// be interpreted as a positive tag if not prefixed with "+".
func isSpecialTestSuiteTag(tag string) bool {
	if tag == "manual" || strings.HasPrefix(tag, "-") || strings.HasPrefix(tag, "+") {
		return true
	}
	_, err := ParseTestSize(tag)
	return err == nil
}

// GetTestSuiteTags returns the tags of a test suite in a form that can
// be provided to test_suite(), which is equivalent to the tags that
// were originally provided. Tags are sorted alphabetically.
func GetTestSuiteTags(testSuite *model_starlark_pb.TestSuite) []string {
	tags := make([]string, 0, len(testSuite.PositiveTags)+len(testSuite.NegativeTags)+len(allTestSizes)+1)
	for _, tag := range testSuite.PositiveTags {
		if isSpecialTestSuiteTag(tag) {
			tag = "+" + tag
		}
		tags = append(tags, tag)
	}
	for _, tag := range testSuite.NegativeTags {
		tags = append(tags, "-"+tag)
	}
	if len(testSuite.Sizes) == 1 {
		// Only tests of a single size are eligible.
		tags = append(tags, FormatTestSize(testSuite.Sizes[0]))
	} else {
		// Tests of multiple sizes are eligible. This can only
		// be expressed by excluding the other sizes.
		for _, size := range allTestSizes {
			if !slices.Contains(testSuite.Sizes, size) {
				tags = append(tags, "-"+FormatTestSize(size))
			}
		}
	}
	if testSuite.Manual {
		tags = append(tags, "manual")
	}
	slices.Sort(tags)
	return tags
}

// GetTestSuiteQueryTags returns the tags of a test suite that are
// matched by the tag() query function, and are reported by attr(),
// which are its positive tags and the "manual" tag. Tags are sorted
// alphabetically and deduplicated.
func GetTestSuiteQueryTags(testSuite *model_starlark_pb.TestSuite) []string {
	tags := testSuite.PositiveTags
	if testSuite.Manual {
		if index, found := slices.BinarySearch(tags, "manual"); !found {
			tags = slices.Insert(slices.Clone(tags), index, "manual")
		}
	}
	return tags
}

// TestSuiteMatchesTest returns true if a test, having a given set of
// tags and size, passes the filters on tags and sizes of a test suite.
// The tags of the test must be sorted alphabetically.
func TestSuiteMatchesTest(testSuite *model_starlark_pb.TestSuite, testTags []string, testSize model_starlark_pb.TestSize) bool {
	if !slices.Contains(testSuite.Sizes, testSize) {
		return false
	}
	for _, tag := range testSuite.PositiveTags {
		if _, found := slices.BinarySearch(testTags, tag); !found {
			return false
		}
	}
	for _, tag := range testSuite.NegativeTags {
		if _, found := slices.BinarySearch(testTags, tag); found {
			return false
		}
	}
	return true
}

// computeTestSuiteTestParentNode generates a parent node of a B-tree
// containing the labels of the tests that are part of a test suite.
func computeTestSuiteTestParentNode[TMetadata model_core.ReferenceMetadata](createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_starlark_pb.TestSuite_Test, object.LocalReference]) model_core.PatchedMessage[*model_starlark_pb.TestSuite_Test, TMetadata] {
	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_starlark_pb.TestSuite_Test {
		return &model_starlark_pb.TestSuite_Test{
			Level: &model_starlark_pb.TestSuite_Test_Parent_{
				Parent: &model_starlark_pb.TestSuite_Test_Parent{
					Reference: patcher.AddDecodableReference(createdObject),
				},
			},
		}
	})
}

// buildTestSuiteTests creates a B-tree containing the labels of the
// tests that are part of a test suite. As the list of tests may be
// large, it is spread out across multiple objects if needed. Labels
// are sorted alphabetically and deduplicated.
func buildTestSuiteTests[TReference any, TMetadata model_core.ReferenceMetadata](tests []string, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_starlark_pb.TestSuite_Test, TMetadata], error) {
	sortedTests := slices.Clone(tests)
	slices.Sort(sortedTests)
	sortedTests = slices.Compact(sortedTests)

	treeBuilder := newBTreeBuilder(
		options,
		/* isParent = */ func(test *model_starlark_pb.TestSuite_Test) bool {
			return test.GetParent() != nil
		},
		btree.Capturing(options.Context, options.ObjectCapturer, computeTestSuiteTestParentNode),
	)
	defer treeBuilder.Discard()
	for _, test := range sortedTests {
		if err := treeBuilder.PushChild(model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.TestSuite_Test{
			Level: &model_starlark_pb.TestSuite_Test_Leaf{
				Leaf: test,
			},
		})); err != nil {
			return model_core.PatchedMessage[[]*model_starlark_pb.TestSuite_Test, TMetadata]{}, err
		}
	}
	return treeBuilder.FinalizeList()
}
