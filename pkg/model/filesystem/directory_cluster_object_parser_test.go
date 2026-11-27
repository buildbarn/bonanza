package filesystem_test

import (
	"testing"

	"bonanza.build/pkg/encoding/varint"
	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"
)

func TestDirectoryClusterObjectParser(t *testing.T) {
	objectParser := model_filesystem.NewDirectoryClusterObjectParser[object.LocalReference]()

	t.Run("InvalidMessage", func(t *testing.T) {
		_, err := objectParser.ParseObject(
			model_core.NewSimpleMessage[object.LocalReference](
				[]byte("Not a valid Protobuf message"),
			),
			nil,
		)
		require.Error(t, err)
	})

	t.Run("NestedDirectories", func(t *testing.T) {
		// Directory "a" is stored inline and contains directory
		// "b", while the entries of directory "c" are stored
		// in another object.
		entries := []*model_filesystem_pb.DirectoryEntry{
			directoryContentsEntry(
				directoryContentsEntry(),
				directoryNodeEntry("b", 0),
			),
			directoryContentsEntry(model_filesystem_pb.DirectoryEntry_builder{
				Parent: model_filesystem_pb.DirectoryEntry_Parent_builder{
					Reference: model_core_pb.DecodableReference_builder{
						Reference: model_core_pb.Reference_builder{Index: 1}.Build(),
					}.Build(),
					FirstName:      "x",
					LeafNodesCount: 100,
				}.Build(),
			}.Build()),
			directoryNodeEntry("a", 1),
			directoryNodeEntry("c", 0),
		}
		var data []byte
		for _, entry := range entries {
			element, err := proto.Marshal(entry)
			require.NoError(t, err)
			data = varint.AppendForward(data, len(element))
			data = append(data, element...)
		}

		cluster, err := objectParser.ParseObject(
			model_core.NewSimpleMessage[object.LocalReference](data),
			nil,
		)
		require.NoError(t, err)

		// Directories should be stored in topological order,
		// with the root directory corresponding to the object
		// in its entirety.
		directories := cluster.Message.Directories
		require.Len(t, directories, 4)
		require.Equal(t, []int{1, 3, -1, -1}, directories[0].ChildDirectoryIndices)
		require.Equal(t, []int{2, -1}, directories[1].ChildDirectoryIndices)
		require.Empty(t, directories[2].ChildDirectoryIndices)
		require.Equal(t, []int{-1}, directories[3].ChildDirectoryIndices)
	})
}
