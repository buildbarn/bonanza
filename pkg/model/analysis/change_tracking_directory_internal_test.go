package analysis

import (
	"context"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

type testChangeTrackingDirectory = changeTrackingDirectory[model_core.CreatedObjectTree, model_core.CreatedObjectTree]

// newTestChunkReference creates a reference to a chunk containing the
// provided data, which can be used as the contents of a small file.
func newTestChunkReference(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree], data string) *model_core_pb.DecodableReference {
	createdObject := util.Must(
		model_core.MarshalAndEncodeDeterministic(
			model_core.NewSimplePatchedMessage[model_core.CreatedObjectTree](model_core.NewRawBinaryMarshaler([]byte(data))),
			util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
			model_encoding.NewChainedDeterministicBinaryEncoder(nil),
		),
	)
	return &model_core_pb.DecodableReference{
		Reference: patcher.AddReference(model_core.MetadataEntry[model_core.CreatedObjectTree]{
			LocalReference: createdObject.Value.GetLocalReference(),
			Metadata:       model_core.CreatedObjectTree(createdObject.Value),
		}),
		DecodingParameters: createdObject.GetDecodingParameters(),
	}
}

// newTestMessage creates a message that may contain references to
// objects, as part of tests.
func newTestMessage[TMessage any](builder func(*model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) TMessage) model_core.Message[TMessage, model_core.CreatedObjectTree] {
	m, metadata := model_core.MustBuildPatchedMessage(builder).SortAndSetReferences()
	return model_core.NewMessage(m.Message, object.OutgoingReferencesList[model_core.CreatedObjectTree](metadata))
}

// newTestFileProperties creates the properties of a file containing
// the provided data. Empty files have no contents.
func newTestFileProperties(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree], data string, isExecutable bool) *model_filesystem_pb.FileProperties {
	properties := &model_filesystem_pb.FileProperties{
		IsExecutable: isExecutable,
	}
	if data != "" {
		properties.Contents = &model_filesystem_pb.FileContents{
			Level: &model_filesystem_pb.FileContents_ChunkReference{
				ChunkReference: newTestChunkReference(patcher, data),
			},
			TotalSizeBytes: uint64(len(data)),
		}
	}
	return properties
}

// newTestFile creates a file that is backed by contents in storage.
func newTestFile(t *testing.T, data string, isExecutable bool) *changeTrackingFile[model_core.CreatedObjectTree, model_core.CreatedObjectTree] {
	f, err := newChangeTrackingFileFromFileProperties[model_core.CreatedObjectTree, model_core.CreatedObjectTree](
		newTestMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_filesystem_pb.FileProperties {
			return newTestFileProperties(patcher, data, isExecutable)
		}),
	)
	require.NoError(t, err)
	return f
}

func newTestComponent(name string) path.Component {
	return path.MustNewComponent(name)
}

func TestChangeTrackingFileIsEqualTo(t *testing.T) {
	t.Run("SameContents", func(t *testing.T) {
		// Files backed by separately created messages with the
		// same contents are equal.
		require.True(t, newTestFile(t, "Hello", false).isEqualTo(newTestFile(t, "Hello", false)))
		require.True(t, newTestFile(t, "Hello", true).isEqualTo(newTestFile(t, "Hello", true)))
	})

	t.Run("DifferentContents", func(t *testing.T) {
		require.False(t, newTestFile(t, "Hello", false).isEqualTo(newTestFile(t, "World", false)))
	})

	t.Run("DifferentExecutableBit", func(t *testing.T) {
		require.False(t, newTestFile(t, "Hello", false).isEqualTo(newTestFile(t, "Hello", true)))
	})

	t.Run("EmptyFiles", func(t *testing.T) {
		require.True(t, newTestFile(t, "", false).isEqualTo(newTestFile(t, "", false)))
		require.False(t, newTestFile(t, "", false).isEqualTo(newTestFile(t, "Hello", false)))
		require.False(t, newTestFile(t, "Hello", false).isEqualTo(newTestFile(t, "", false)))
	})

	t.Run("PatchedContents", func(t *testing.T) {
		// Files whose contents were modified locally can only
		// be compared against themselves.
		f1 := &changeTrackingFile[model_core.CreatedObjectTree, model_core.CreatedObjectTree]{
			contents: patchedFileContents[model_core.CreatedObjectTree, model_core.CreatedObjectTree]{
				offsetBytes: 0,
				sizeBytes:   5,
			},
		}
		f2 := &changeTrackingFile[model_core.CreatedObjectTree, model_core.CreatedObjectTree]{
			contents: patchedFileContents[model_core.CreatedObjectTree, model_core.CreatedObjectTree]{
				offsetBytes: 0,
				sizeBytes:   5,
			},
		}
		require.True(t, f1.isEqualTo(f1))
		require.False(t, f1.isEqualTo(f2))
		require.False(t, f1.isEqualTo(newTestFile(t, "Hello", false)))
		require.False(t, newTestFile(t, "Hello", false).isEqualTo(f1))
	})
}

func TestChangeTrackingDirectoryMergeDirectory(t *testing.T) {
	loadOptions := &changeTrackingDirectoryLoadOptions[model_core.CreatedObjectTree]{
		context: context.Background(),
	}

	// newDirectory creates a directory that contains a directory
	// named "dir", which in turn contains the provided entries.
	newDirectory := func(files, symlinks map[string]string, directories []string) *testChangeTrackingDirectory {
		child := &testChangeTrackingDirectory{}
		for name, data := range files {
			child.setFileSimple(newTestComponent(name), newTestFile(t, data, false))
		}
		for name, target := range symlinks {
			child.setSymlinkSimple(newTestComponent(name), path.UNIXFormat.NewParser(target))
		}
		for _, name := range directories {
			child.setDirectorySimple(newTestComponent(name), &testChangeTrackingDirectory{})
		}
		parent := &testChangeTrackingDirectory{}
		parent.setDirectorySimple(newTestComponent("dir"), child)
		return parent
	}

	t.Run("Disjoint", func(t *testing.T) {
		d := newDirectory(map[string]string{"a": "Hello"}, nil, nil)
		require.NoError(t, d.mergeDirectory(newDirectory(map[string]string{"b": "World"}, map[string]string{"c": "a"}, []string{"d"}), loadOptions))

		child := d.directories[newTestComponent("dir")]
		require.Len(t, child.files, 2)
		require.Len(t, child.symlinks, 1)
		require.Len(t, child.directories, 1)
	})

	t.Run("IdenticalEntries", func(t *testing.T) {
		d := newDirectory(map[string]string{"a": "Hello"}, map[string]string{"b": "../x"}, []string{"c"})
		require.NoError(t, d.mergeDirectory(newDirectory(map[string]string{"a": "Hello"}, map[string]string{"b": "../x"}, []string{"c"}), loadOptions))
	})

	t.Run("FileWithDifferentContents", func(t *testing.T) {
		d := newDirectory(map[string]string{"a": "Hello"}, nil, nil)
		require.EqualError(
			t,
			d.mergeDirectory(newDirectory(map[string]string{"a": "World"}, nil, nil), loadOptions),
			"file \"dir/a\" conflicts with an existing file with different contents",
		)
	})

	t.Run("SymlinkWithDifferentTarget", func(t *testing.T) {
		d := newDirectory(nil, map[string]string{"a": "../x"}, nil)
		require.EqualError(
			t,
			d.mergeDirectory(newDirectory(nil, map[string]string{"a": "../y"}, nil), loadOptions),
			"symbolic link \"dir/a\" conflicts with an existing symbolic link with a different target",
		)
	})

	t.Run("TypeMismatches", func(t *testing.T) {
		for _, tc := range []struct {
			name          string
			existing      *testChangeTrackingDirectory
			other         *testChangeTrackingDirectory
			expectedError string
		}{
			{
				"FileOverSymlink",
				newDirectory(nil, map[string]string{"a": "x"}, nil),
				newDirectory(map[string]string{"a": "Hello"}, nil, nil),
				"file \"dir/a\" conflicts with an existing symbolic link",
			},
			{
				"FileOverDirectory",
				newDirectory(nil, nil, []string{"a"}),
				newDirectory(map[string]string{"a": "Hello"}, nil, nil),
				"file \"dir/a\" conflicts with an existing directory",
			},
			{
				"SymlinkOverFile",
				newDirectory(map[string]string{"a": "Hello"}, nil, nil),
				newDirectory(nil, map[string]string{"a": "x"}, nil),
				"symbolic link \"dir/a\" conflicts with an existing file",
			},
			{
				"SymlinkOverDirectory",
				newDirectory(nil, nil, []string{"a"}),
				newDirectory(nil, map[string]string{"a": "x"}, nil),
				"symbolic link \"dir/a\" conflicts with an existing directory",
			},
			{
				"DirectoryOverFile",
				newDirectory(map[string]string{"a": "Hello"}, nil, nil),
				newDirectory(nil, nil, []string{"a"}),
				"directory \"dir/a\" conflicts with an existing file",
			},
			{
				"DirectoryOverSymlink",
				newDirectory(nil, map[string]string{"a": "x"}, nil),
				newDirectory(nil, nil, []string{"a"}),
				"directory \"dir/a\" conflicts with an existing symbolic link",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				require.EqualError(t, tc.existing.mergeDirectory(tc.other, loadOptions), tc.expectedError)
			})
		}
	})
}

func TestChangeTrackingDirectoryMergeContents(t *testing.T) {
	loadOptions := &changeTrackingDirectoryLoadOptions[model_core.CreatedObjectTree]{
		context: context.Background(),
	}

	// newContents creates directory contents containing a single
	// file at path "dir/sub/a", stored in inline messages.
	newContents := func(data string, isExecutable bool) model_core.Message[*model_filesystem_pb.DirectoryContents, model_core.CreatedObjectTree] {
		return newTestMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_filesystem_pb.DirectoryContents {
			return &model_filesystem_pb.DirectoryContents{
				Directories: []*model_filesystem_pb.DirectoryNode{{
					Name: "dir",
					Directory: &model_filesystem_pb.Directory{
						Contents: &model_filesystem_pb.Directory_ContentsInline{
							ContentsInline: &model_filesystem_pb.DirectoryContents{
								Directories: []*model_filesystem_pb.DirectoryNode{{
									Name: "sub",
									Directory: &model_filesystem_pb.Directory{
										Contents: &model_filesystem_pb.Directory_ContentsInline{
											ContentsInline: &model_filesystem_pb.DirectoryContents{
												Leaves: &model_filesystem_pb.DirectoryContents_LeavesInline{
													LeavesInline: &model_filesystem_pb.Leaves{
														Files: []*model_filesystem_pb.FileNode{{
															Name:       "a",
															Properties: newTestFileProperties(patcher, data, isExecutable),
														}},
													},
												},
											},
										},
									},
								}},
								Leaves: &model_filesystem_pb.DirectoryContents_LeavesInline{
									LeavesInline: &model_filesystem_pb.Leaves{},
								},
							},
						},
					},
				}},
				Leaves: &model_filesystem_pb.DirectoryContents_LeavesInline{
					LeavesInline: &model_filesystem_pb.Leaves{},
				},
			}
		})
	}

	t.Run("IdenticalFile", func(t *testing.T) {
		// Merging the same file twice should succeed. Load the
		// directories in between, so that merging can't skip
		// directories whose messages are equal, and the
		// contents of the files need to be compared.
		var d testChangeTrackingDirectory
		require.NoError(t, d.mergeContents(newContents("Hello", false), loadOptions))
		dir := d.directories[newTestComponent("dir")]
		require.NoError(t, dir.maybeLoadContents(loadOptions))
		sub := dir.directories[newTestComponent("sub")]
		require.NoError(t, sub.maybeLoadContents(loadOptions))

		require.NoError(t, d.mergeContents(newContents("Hello", false), loadOptions))
		require.Len(t, sub.files, 1)
	})

	t.Run("FileWithDifferentContents", func(t *testing.T) {
		// The full path of the conflicting file should be
		// reported.
		var d testChangeTrackingDirectory
		require.NoError(t, d.mergeContents(newContents("Hello", false), loadOptions))
		require.EqualError(
			t,
			d.mergeContents(newContents("World", false), loadOptions),
			"file \"dir/sub/a\" conflicts with an existing file with different contents",
		)
	})

	t.Run("FileWithDifferentExecutableBit", func(t *testing.T) {
		var d testChangeTrackingDirectory
		require.NoError(t, d.mergeContents(newContents("Hello", false), loadOptions))
		require.EqualError(
			t,
			d.mergeContents(newContents("Hello", true), loadOptions),
			"file \"dir/sub/a\" conflicts with an existing file with different contents",
		)
	})

	t.Run("IntoInMemoryDirectory", func(t *testing.T) {
		// Conflicts should also be detected when merging into
		// directories that were constructed in memory.
		sub := &testChangeTrackingDirectory{}
		sub.setFileSimple(newTestComponent("a"), newTestFile(t, "Hello", false))
		dir := &testChangeTrackingDirectory{}
		dir.setDirectorySimple(newTestComponent("sub"), sub)
		var d testChangeTrackingDirectory
		d.setDirectorySimple(newTestComponent("dir"), dir)

		require.NoError(t, d.mergeContents(newContents("Hello", false), loadOptions))
		require.EqualError(
			t,
			d.mergeContents(newContents("World", false), loadOptions),
			"file \"dir/sub/a\" conflicts with an existing file with different contents",
		)
	})
}
