package filesystem_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/dag"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

// symlinkEntry creates a DirectoryEntry for a symbolic link.
func symlinkEntry(name, target string) *model_filesystem_pb.DirectoryEntry {
	return model_filesystem_pb.DirectoryEntry_builder{
		Node: model_filesystem_pb.DirectoryEntry_Node_builder{
			Name: name,
			Symlink: model_filesystem_pb.SymlinkProperties_builder{
				Target: target,
			}.Build(),
		}.Build(),
	}.Build()
}

// directoryNodeEntry creates a DirectoryEntry for a subdirectory node.
func directoryNodeEntry(name string, directoriesCount uint32) *model_filesystem_pb.DirectoryEntry {
	return model_filesystem_pb.DirectoryEntry_builder{
		Node: model_filesystem_pb.DirectoryEntry_Node_builder{
			Name: name,
			Directory: model_filesystem_pb.DirectoryProperties_builder{
				DirectoriesCount: directoriesCount,
			}.Build(),
		}.Build(),
	}.Build()
}

// directoryContentsEntry creates a DirectoryEntry containing the
// contents of a subdirectory.
func directoryContentsEntry(entries ...*model_filesystem_pb.DirectoryEntry) *model_filesystem_pb.DirectoryEntry {
	return model_filesystem_pb.DirectoryEntry_builder{
		DirectoryContents: model_filesystem_pb.DirectoryEntry_DirectoryContents_builder{
			Entries: entries,
		}.Build(),
	}.Build()
}

func TestNewCreatedDirectoryBare(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		cd, err := model_filesystem.NewCreatedDirectoryBare(
			model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](
				[]*model_filesystem_pb.DirectoryEntry(nil),
			),
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(
			t,
			&wrapperspb.UInt32Value{Value: 0},
			cd.MaximumSymlinkEscapementLevels,
		)
	})

	t.Run("RootSymlinkNotEscaping", func(t *testing.T) {
		cd, err := model_filesystem.NewCreatedDirectoryBare(
			model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](
				[]*model_filesystem_pb.DirectoryEntry{
					symlinkEntry("hello", "target"),
				},
			),
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(
			t,
			&wrapperspb.UInt32Value{Value: 0},
			cd.MaximumSymlinkEscapementLevels,
		)
	})

	t.Run("RootSymlinkEscapingSingleLevel", func(t *testing.T) {
		cd, err := model_filesystem.NewCreatedDirectoryBare(
			model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](
				[]*model_filesystem_pb.DirectoryEntry{
					symlinkEntry("hello", "../target"),
				},
			),
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(
			t,
			&wrapperspb.UInt32Value{Value: 1},
			cd.MaximumSymlinkEscapementLevels,
		)
	})

	t.Run("RootSymlinkAbsolute", func(t *testing.T) {
		cd, err := model_filesystem.NewCreatedDirectoryBare(
			model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](
				[]*model_filesystem_pb.DirectoryEntry{
					symlinkEntry("hello", "/target"),
				},
			),
		)
		require.NoError(t, err)
		require.Nil(t, cd.MaximumSymlinkEscapementLevels)
	})

	t.Run("ParentEscapingMultipleLevels", func(t *testing.T) {
		// Parent nodes in the directory's own B-tree are
		// relative to the directory itself.
		cd, err := model_filesystem.NewCreatedDirectoryBare(
			model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](
				[]*model_filesystem_pb.DirectoryEntry{model_filesystem_pb.DirectoryEntry_builder{
					Parent: model_filesystem_pb.DirectoryEntry_Parent_builder{
						Reference: model_core_pb.DecodableReference_builder{
							Reference: model_core_pb.Reference_builder{Index: 1}.Build(),
						}.Build(),
						FirstName:                      "hello",
						DirectoryNodesCount:            1,
						DirectoryContentsCount:         1,
						MaximumSymlinkEscapementLevels: &wrapperspb.UInt32Value{Value: 3},
					}.Build(),
				}.Build()},
			),
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(
			t,
			&wrapperspb.UInt32Value{Value: 3},
			cd.MaximumSymlinkEscapementLevels,
		)
	})

	t.Run("CollapsedChildEscapingMultipleLevels", func(t *testing.T) {
		// Parent nodes of a collapsed subdirectory are relative
		// to the subdirectory, meaning they need to be
		// decremented by one.
		cd, err := model_filesystem.NewCreatedDirectoryBare(
			model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](
				[]*model_filesystem_pb.DirectoryEntry{
					directoryContentsEntry(model_filesystem_pb.DirectoryEntry_builder{
						Parent: model_filesystem_pb.DirectoryEntry_Parent_builder{
							Reference: model_core_pb.DecodableReference_builder{
								Reference: model_core_pb.Reference_builder{Index: 1}.Build(),
							}.Build(),
							FirstName:                      "a",
							LeafNodesCount:                 100,
							MaximumSymlinkEscapementLevels: &wrapperspb.UInt32Value{Value: 3},
						}.Build(),
					}.Build()),
					directoryNodeEntry("hello", 0),
				},
			),
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(
			t,
			&wrapperspb.UInt32Value{Value: 2},
			cd.MaximumSymlinkEscapementLevels,
		)
	})

	t.Run("InlineChildEscapingMultipleLevels", func(t *testing.T) {
		cd, err := model_filesystem.NewCreatedDirectoryBare(
			model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](
				[]*model_filesystem_pb.DirectoryEntry{
					directoryContentsEntry(
						symlinkEntry("hello", "../../../target"),
					),
					directoryNodeEntry("hello", 0),
				},
			),
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(
			t,
			&wrapperspb.UInt32Value{Value: 2},
			cd.MaximumSymlinkEscapementLevels,
		)
	})
}
