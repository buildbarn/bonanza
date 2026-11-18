package filesystem_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	bb_path "github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/stretchr/testify/require"
)

// createdObjectTreeCapturer is an ObjectCapturer that uses
// CreatedObjectTree both as the reference and metadata type.
type createdObjectTreeCapturer struct{}

func (createdObjectTreeCapturer) CaptureCreatedObject(ctx context.Context, createdObject model_core.CreatedObject[model_core.CreatedObjectTree]) (model_core.CreatedObjectTree, error) {
	return model_core.CreatedObjectTree(createdObject), nil
}

func (createdObjectTreeCapturer) CaptureExistingObject(reference model_core.CreatedObjectTree) model_core.CreatedObjectTree {
	return reference
}

// countingObjectReader wraps createdObjectReader, counting the number
// of objects that are read.
type countingObjectReader struct {
	reads int
}

func (r *countingObjectReader) ReadObject(ctx context.Context, reference model_core.CreatedObjectTree) (model_core.Message[[]byte, model_core.CreatedObjectTree], error) {
	r.reads++
	return createdObjectReader{}.ReadObject(ctx, reference)
}

// mergedDirectoryTester contains the state that is shared by the tests
// of MergedDirectory.
type mergedDirectoryTester struct {
	t          *testing.T
	ctx        context.Context
	parameters *model_filesystem.DirectoryCreationParameters
	objects    *countingObjectReader
	reader     model_parser.MessageObjectReader[model_core.CreatedObjectTree, []*model_filesystem_pb.DirectoryEntry]
}

func newMergedDirectoryTester(t *testing.T) *mergedDirectoryTester {
	parameters, err := model_filesystem.NewDirectoryCreationParametersFromProto(
		&model_filesystem_pb.DirectoryCreationParameters{
			Access: &model_filesystem_pb.DirectoryAccessParameters{},
			DirectoriesParameters: &model_chunking_pb.Parameters{
				MinimumSizeBytes: 1024,
				HorizonSizeBytes: 8 * 1024,
			},
		},
		object.SHA256V1ReferenceFormat,
		model_encoding.NewBinaryEncoderFactory(true),
	)
	require.NoError(t, err)
	objects := &countingObjectReader{}
	return &mergedDirectoryTester{
		t:          t,
		ctx:        context.Background(),
		parameters: parameters,
		objects:    objects,
		reader: model_parser.LookupParsedObjectReader(
			model_parser.NewParsedObjectPoolIngester[model_core.CreatedObjectTree](
				/* pool = */ nil,
				objects,
			),
			model_parser.NewProtoListObjectParser[model_core.CreatedObjectTree, model_filesystem_pb.DirectoryEntry](),
		),
	}
}

// testTree describes a directory hierarchy. Keys are names. Values are
// either a nested testTree (directories), "file", "executable" or
// "-> target" (symbolic links).
type testTree map[string]any

// create the B-tree of a directory described by a testTree.
func (mt *mergedDirectoryTester) create(tree testTree) model_filesystem.CreatedDirectory[model_core.CreatedObjectTree] {
	names := make([]string, 0, len(tree))
	for name := range tree {
		names = append(names, name)
	}
	sort.Strings(names)

	var children []model_filesystem.CreatedDirectory[model_core.CreatedObjectTree]
	for _, name := range names {
		if child, ok := tree[name].(testTree); ok {
			children = append(children, mt.create(child))
		}
	}
	var nodes []model_filesystem.DirectoryNode[model_core.CreatedObjectTree]
	childIndex := 0
	for _, name := range names {
		switch value := tree[name].(type) {
		case testTree:
			nodes = append(nodes, model_filesystem.DirectoryNode[model_core.CreatedObjectTree]{
				Name:      name,
				Directory: &children[childIndex],
			})
			childIndex++
		case string:
			node := &model_filesystem_pb.DirectoryEntry_Node{Name: name}
			if target, ok := strings.CutPrefix(value, "-> "); ok {
				node.Kind = &model_filesystem_pb.DirectoryEntry_Node_Symlink{
					Symlink: &model_filesystem_pb.SymlinkProperties{Target: target},
				}
			} else {
				node.Kind = &model_filesystem_pb.DirectoryEntry_Node_File{
					File: &model_filesystem_pb.FileProperties{IsExecutable: value == "executable"},
				}
			}
			nodes = append(nodes, model_filesystem.DirectoryNode[model_core.CreatedObjectTree]{
				Name: name,
				Leaf: model_core.NewSimplePatchedMessage[model_core.CreatedObjectTree](node),
			})
		}
	}
	createdDirectory, err := model_filesystem.CreateDirectory(mt.ctx, mt.parameters, createdObjectTreeCapturer{}, nodes)
	require.NoError(mt.t, err)
	return createdDirectory
}

// contents returns the top level of the B-tree of a directory described
// by a testTree, in literal form.
func (mt *mergedDirectoryTester) contents(tree testTree) model_core.Message[[]*model_filesystem_pb.DirectoryEntry, model_core.CreatedObjectTree] {
	createdDirectory := mt.create(tree)
	message, metadata := createdDirectory.Message.SortAndSetReferences()
	return model_core.NewMessage(message.Message, object.OutgoingReferencesList[model_core.CreatedObjectTree](metadata))
}

// reference returns a DirectoryReference of a directory described by a
// testTree, whose top level is stored in a separate object.
func (mt *mergedDirectoryTester) reference(tree testTree) model_core.Message[*model_filesystem_pb.DirectoryReference, model_core.CreatedObjectTree] {
	createdDirectory := mt.create(tree)
	directoryReference, err := createdDirectory.CaptureDirectoryReference(mt.ctx, mt.parameters, createdObjectTreeCapturer{})
	require.NoError(mt.t, err)
	message, metadata := directoryReference.SortAndSetReferences()
	return model_core.NewMessage(message.Message, object.OutgoingReferencesList[model_core.CreatedObjectTree](metadata))
}

// flatten converts the result of CreateMergedDirectory() to a testTree.
func (mt *mergedDirectoryTester) flatten(createdDirectory model_filesystem.CreatedDirectory[model_core.CreatedObjectTree]) testTree {
	message, metadata := createdDirectory.Message.SortAndSetReferences()
	return mt.flattenContents(model_core.NewMessage(message.Message, object.OutgoingReferencesList[model_core.CreatedObjectTree](metadata)))
}

func (mt *mergedDirectoryTester) flattenContents(contents model_core.Message[[]*model_filesystem_pb.DirectoryEntry, model_core.CreatedObjectTree]) testTree {
	tree := testTree{}
	var errIter error
	for node := range model_filesystem.DirectoryAllNodes(mt.ctx, mt.reader, contents, &errIter) {
		switch kind := node.Message.Kind.(type) {
		case *model_filesystem_pb.DirectoryEntry_Node_File:
			if kind.File.IsExecutable {
				tree[node.Message.Name] = "executable"
			} else {
				tree[node.Message.Name] = "file"
			}
		case *model_filesystem_pb.DirectoryEntry_Node_Symlink:
			tree[node.Message.Name] = "-> " + kind.Symlink.Target
		}
	}
	require.NoError(mt.t, errIter)
	for node, child := range model_filesystem.DirectoryAllChildren(mt.ctx, mt.reader, contents, &errIter) {
		directoriesCount, err := model_filesystem.GetDirectoryDirectoriesCount(child.Message)
		require.NoError(mt.t, err)
		require.Equal(mt.t, node.Message.GetDirectory().DirectoriesCount, directoriesCount, "Directories count of %#v", node.Message.Name)
		tree[node.Message.Name] = mt.flattenContents(child)
	}
	require.NoError(mt.t, errIter)
	return tree
}

func (mt *mergedDirectoryTester) merge(policy model_filesystem.MergeConflictPolicy, d *model_filesystem.MergedDirectory[model_core.CreatedObjectTree]) (testTree, error) {
	createdDirectory, err := model_filesystem.CreateMergedDirectory(mt.ctx, mt.reader, mt.parameters, createdObjectTreeCapturer{}, policy, d)
	if err != nil {
		return nil, err
	}
	return mt.flatten(createdDirectory), nil
}

func TestMergedDirectory(t *testing.T) {
	treeA := testTree{
		"common": testTree{
			"from_a.txt": "file",
			"both.txt":   "file",
			"nested":     testTree{"a.txt": "file"},
		},
		"only_a":  testTree{"x": "file"},
		"link":    "-> common/from_a.txt",
		"top.txt": "file",
	}
	treeB := testTree{
		"common": testTree{
			"from_b.txt": "file",
			"both.txt":   "executable",
			"nested":     testTree{"b.txt": "file"},
		},
		"only_b":  testTree{"y": "file"},
		"top.txt": testTree{"z": "file"},
	}

	t.Run("Empty", func(t *testing.T) {
		mt := newMergedDirectoryTester(t)
		tree, err := mt.merge(model_filesystem.MergeConflictPolicyError, &model_filesystem.MergedDirectory[model_core.CreatedObjectTree]{})
		require.NoError(t, err)
		require.Equal(t, testTree{}, tree)
	})

	t.Run("SingleSource", func(t *testing.T) {
		// A directory with a single source should be embedded
		// as is.
		mt := newMergedDirectoryTester(t)
		var d model_filesystem.MergedDirectory[model_core.CreatedObjectTree]
		require.NoError(t, d.MergeDirectoryReference(mt.reference(treeA)))
		tree, err := mt.merge(model_filesystem.MergeConflictPolicyError, &d)
		require.NoError(t, err)
		require.Equal(t, treeA, tree)
	})

	t.Run("KeepFirst", func(t *testing.T) {
		// Directories should be merged recursively. Conflicts
		// should be resolved by keeping the entry that was
		// merged first.
		mt := newMergedDirectoryTester(t)
		var d model_filesystem.MergedDirectory[model_core.CreatedObjectTree]
		require.NoError(t, d.MergeDirectoryReference(mt.reference(treeA)))
		d.MergeDirectoryContents(mt.contents(treeB))
		tree, err := mt.merge(model_filesystem.MergeConflictPolicyKeepFirst, &d)
		require.NoError(t, err)
		require.Equal(t, testTree{
			"common": testTree{
				"from_a.txt": "file",
				"from_b.txt": "file",
				"both.txt":   "file",
				"nested":     testTree{"a.txt": "file", "b.txt": "file"},
			},
			"only_a":  testTree{"x": "file"},
			"only_b":  testTree{"y": "file"},
			"link":    "-> common/from_a.txt",
			"top.txt": "file",
		}, tree)
	})

	t.Run("ErrorOnDifferentFiles", func(t *testing.T) {
		mt := newMergedDirectoryTester(t)
		var d model_filesystem.MergedDirectory[model_core.CreatedObjectTree]
		d.MergeDirectoryContents(mt.contents(testTree{"dir": testTree{"f": "file"}}))
		d.MergeDirectoryContents(mt.contents(testTree{"dir": testTree{"f": "executable"}}))
		_, err := mt.merge(model_filesystem.MergeConflictPolicyError, &d)
		require.ErrorContains(t, err, `Conflicting files or symbolic links at "dir/f"`)
	})

	t.Run("ErrorOnFileAndDirectory", func(t *testing.T) {
		mt := newMergedDirectoryTester(t)
		var d model_filesystem.MergedDirectory[model_core.CreatedObjectTree]
		d.MergeDirectoryContents(mt.contents(testTree{"x": "file"}))
		d.MergeDirectoryContents(mt.contents(testTree{"x": testTree{"y": "file"}}))
		_, err := mt.merge(model_filesystem.MergeConflictPolicyError, &d)
		require.ErrorContains(t, err, `Directory "x" conflicts with a file or symbolic link`)
	})

	t.Run("IdenticalFilesAndSymlinks", func(t *testing.T) {
		// Identical files and symbolic links should not be
		// considered conflicts.
		mt := newMergedDirectoryTester(t)
		var d model_filesystem.MergedDirectory[model_core.CreatedObjectTree]
		d.MergeDirectoryContents(mt.contents(testTree{"f": "executable", "l": "-> a/b", "only1": "file"}))
		d.MergeDirectoryContents(mt.contents(testTree{"f": "executable", "l": "-> a/b", "only2": "file"}))
		tree, err := mt.merge(model_filesystem.MergeConflictPolicyError, &d)
		require.NoError(t, err)
		require.Equal(t, testTree{"f": "executable", "l": "-> a/b", "only1": "file", "only2": "file"}, tree)
	})

	t.Run("Deduplication", func(t *testing.T) {
		// Merging a directory that was already merged should
		// not cause it to be loaded again.
		mt := newMergedDirectoryTester(t)
		referenceA, referenceB := mt.reference(treeA), mt.reference(treeB)

		var d1 model_filesystem.MergedDirectory[model_core.CreatedObjectTree]
		require.NoError(t, d1.MergeDirectoryReference(referenceA))
		require.NoError(t, d1.MergeDirectoryReference(referenceB))
		tree1, err := mt.merge(model_filesystem.MergeConflictPolicyKeepFirst, &d1)
		require.NoError(t, err)
		reads1 := mt.objects.reads

		var d2 model_filesystem.MergedDirectory[model_core.CreatedObjectTree]
		require.NoError(t, d2.MergeDirectoryReference(referenceA))
		require.NoError(t, d2.MergeDirectoryReference(referenceB))
		require.NoError(t, d2.MergeDirectoryReference(referenceA))
		tree2, err := mt.merge(model_filesystem.MergeConflictPolicyKeepFirst, &d2)
		require.NoError(t, err)
		require.Equal(t, tree1, tree2)
		require.Equal(t, reads1, mt.objects.reads-reads1)
	})

	t.Run("Explicit", func(t *testing.T) {
		// Directories and files can be created explicitly.
		// These follow the same precedence rules as directories
		// that are merged.
		mt := newMergedDirectoryTester(t)
		var d model_filesystem.MergedDirectory[model_core.CreatedObjectTree]
		external := d.GetOrCreateDirectory(bb_path.MustNewComponent("external"))
		repo := external.GetOrCreateDirectory(bb_path.MustNewComponent("repo+"))
		require.NoError(t, repo.MergeDirectoryReference(mt.reference(treeA)))
		repo.AddFile(bb_path.MustNewComponent("top.txt"), model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_filesystem_pb.FileProperties{IsExecutable: true}))
		repo.AddFile(bb_path.MustNewComponent("extra.txt"), model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_filesystem_pb.FileProperties{IsExecutable: true}))
		require.NoError(t, repo.AddSymlink(bb_path.MustNewComponent("extra_link"), bb_path.UNIXFormat.NewParser("a//b/./c")))
		d.MergeDirectoryContents(mt.contents(testTree{
			"external": testTree{"other+": testTree{"o": "file"}},
		}))
		require.Same(t, external, d.GetOrCreateDirectory(bb_path.MustNewComponent("external")))

		tree, err := mt.merge(model_filesystem.MergeConflictPolicyKeepFirst, &d)
		require.NoError(t, err)
		expectedRepo := testTree{}
		for name, value := range treeA {
			expectedRepo[name] = value
		}
		expectedRepo["extra.txt"] = "executable"
		expectedRepo["extra_link"] = "-> a/b/c"
		require.Equal(t, testTree{
			"external": testTree{
				"repo+":  expectedRepo,
				"other+": testTree{"o": "file"},
			},
		}, tree)
	})

	t.Run("LargeDirectories", func(t *testing.T) {
		// Directories whose B-trees contain parent nodes
		// should be merged correctly. This requires locating
		// the contents of subdirectories across objects.
		mt := newMergedDirectoryTester(t)
		tree1, tree2, expected := testTree{}, testTree{}, testTree{}
		for i := 0; i < 500; i++ {
			name := fmt.Sprintf("dir%04d", i)
			tree1[name] = testTree{"one": "file"}
			expectedChild := testTree{"one": "file"}
			if i%3 == 0 {
				tree2[name] = testTree{"two": "file"}
				expectedChild["two"] = "file"
			}
			expected[name] = expectedChild
		}
		contents1 := mt.contents(tree1)
		sawParent := false
		for _, entry := range contents1.Message {
			if entry.GetParent() != nil {
				sawParent = true
			}
		}
		require.True(t, sawParent)

		var d model_filesystem.MergedDirectory[model_core.CreatedObjectTree]
		d.MergeDirectoryContents(contents1)
		d.MergeDirectoryContents(mt.contents(tree2))
		tree, err := mt.merge(model_filesystem.MergeConflictPolicyError, &d)
		require.NoError(t, err)
		require.Equal(t, expected, tree)
	})
}
