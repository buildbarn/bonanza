package analysis_test

import (
	"encoding"
	"testing"

	"bonanza.build/pkg/glob"
	"bonanza.build/pkg/model/analysis"
	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestPackagesAtAndBelow(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	// packageDirectory creates a directory that contains a BUILD
	// file, followed by a number of child directories.
	buildFileLeaves := &model_filesystem_pb.DirectoryContents_LeavesInline{
		LeavesInline: &model_filesystem_pb.Leaves{
			Files: []*model_filesystem_pb.FileNode{{
				Name:       "BUILD.bazel",
				Properties: &model_filesystem_pb.FileProperties{},
			}},
		},
	}
	packageDirectory := func(children ...*model_filesystem_pb.DirectoryNode) *model_filesystem_pb.DirectoryContents {
		return &model_filesystem_pb.DirectoryContents{
			Directories: children,
			Leaves:      buildFileLeaves,
		}
	}
	plainDirectory := func(children ...*model_filesystem_pb.DirectoryNode) *model_filesystem_pb.DirectoryContents {
		return &model_filesystem_pb.DirectoryContents{
			Directories: children,
			Leaves:      emptyLeaves,
		}
	}

	// compute computes the packages below "@@main+//foo" for a
	// directory hierarchy where "foo" has the provided children.
	compute := func(t *testing.T, fooChildren []*model_filesystem_pb.DirectoryNode, includes, excludes []string) (*model_analysis_pb.PackagesAtAndBelow_Value, error) {
		e := NewMockPackagesAtAndBelowEnvironmentForTesting(ctrl)
		e.EXPECT().GetDirectoryAccessParametersObject(
			testutil.EqProto(t, &model_analysis_pb.DirectoryAccessParameters_Key{}),
		).Return(&analysis.DirectoryReaders[model_core.CreatedObjectTree]{
			DirectoryContents: model_parser.LookupParsedObjectReader(
				bct.parsedObjectPoolIngester,
				model_parser.NewProtoObjectParser[model_core.CreatedObjectTree, model_filesystem_pb.DirectoryContents](),
			),
		}, true)
		e.EXPECT().GetRepoValue(
			testutil.EqProto(t, &model_analysis_pb.Repo_Key{CanonicalRepo: "main+"}),
		).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.Repo_Value {
			return &model_analysis_pb.Repo_Value{
				RootDirectoryReference: &model_filesystem_pb.DirectoryReference{
					Reference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
						return model_core.NewProtoBinaryMarshaler(plainDirectory(
							directoryNode("foo", packageDirectory(fooChildren...)),
						))
					})),
				},
			}
		}))

		var pattern []byte
		if includes != nil {
			pattern = util.Must(glob.NewNFAFromPatterns(includes, excludes)).Bytes()
		}
		value, err := bct.computer.ComputePackagesAtAndBelowValue(ctx, &model_analysis_pb.PackagesAtAndBelow_Key{
			BasePackage: "@@main+//foo",
			Pattern:     pattern,
		}, e)
		if err != nil {
			return nil, err
		}
		return value.Message, nil
	}

	// Directory hierarchy below "@@main+//foo" containing packages
	// "a", "b/c", "b/d/e" and "x". "a/nested" is a package as well,
	// but it should not be returned, as it is contained in "a".
	fooChildren := []*model_filesystem_pb.DirectoryNode{
		directoryNode("a", packageDirectory(
			directoryNode("nested", packageDirectory()),
		)),
		directoryNode("b", plainDirectory(
			directoryNode("c", packageDirectory()),
			directoryNode("d", plainDirectory(
				directoryNode("e", packageDirectory()),
			)),
		)),
		directoryNode("x", packageDirectory()),
	}

	t.Run("NoPattern", func(t *testing.T) {
		// Providing a pattern is mandatory.
		_, err := compute(t, fooChildren, nil, nil)
		require.ErrorContains(t, err, "invalid pattern")
	})

	for name, testCase := range map[string]struct {
		includes []string
		excludes []string
		expected []string
	}{
		"All":            {includes: []string{"**"}, expected: []string{"a", "b/c", "b/d/e", "x"}},
		"SingleLevel":    {includes: []string{"*"}, expected: []string{"a", "x"}},
		"Prefix":         {includes: []string{"b/*"}, expected: []string{"b/c"}},
		"Exclude":        {includes: []string{"**"}, excludes: []string{"b/d/**"}, expected: []string{"a", "b/c", "x"}},
		"NestedPackage":  {includes: []string{"a/nested"}, expected: nil},
		"NoMatches":      {includes: []string{"y"}, expected: nil},
		"ExactDirectory": {includes: []string{"b/d/e"}, expected: []string{"b/d/e"}},
	} {
		t.Run(name, func(t *testing.T) {
			value, err := compute(t, fooChildren, testCase.includes, testCase.excludes)
			require.NoError(t, err)
			testutil.RequireEqualProto(t, &model_analysis_pb.PackagesAtAndBelow_Value{
				PackageAtBasePackage:     true,
				PackagesBelowBasePackage: testCase.expected,
			}, value)
		})
	}

	t.Run("Pruning", func(t *testing.T) {
		// Directories that cannot contain any matches should
		// not be loaded. Directory "broken" has invalid
		// contents, causing an error if it is loaded.
		children := append([]*model_filesystem_pb.DirectoryNode{
			{Name: "broken", Directory: &model_filesystem_pb.Directory{}},
		}, fooChildren...)

		_, err := compute(t, children, []string{"**"}, nil)
		require.Error(t, err)

		value, err := compute(t, children, []string{"a", "b/**"}, nil)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.PackagesAtAndBelow_Value{
			PackageAtBasePackage:     true,
			PackagesBelowBasePackage: []string{"a", "b/c", "b/d/e"},
		}, value)
	})
}
