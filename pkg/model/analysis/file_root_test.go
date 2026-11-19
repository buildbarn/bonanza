package analysis_test

import (
	"encoding"
	"sort"
	"testing"

	model_analysis "bonanza.build/pkg/model/analysis"
	model_core "bonanza.build/pkg/model/core"
	model_format "bonanza.build/pkg/model/format"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/types/known/wrapperspb"

	"go.uber.org/mock/gomock"
)

// testFileNode is a regular file that can be added to a directory
// using testLeaves.
type testFileNode struct {
	Name       string
	Properties *model_filesystem_pb.FileProperties
}

// testSymlinkNode is a symbolic link that can be added to a directory
// using testLeaves.
type testSymlinkNode struct {
	Name   string
	Target string
}

// testLeaves contains the files and symbolic links of a directory
// that is constructed using newTestDirectory().
type testLeaves struct {
	Files    []*testFileNode
	Symlinks []*testSymlinkNode
}

// emptyLeaves can be provided to newTestDirectory() to indicate that a
// directory contains no files or symlinks.
var emptyLeaves = &testLeaves{}

// newTestDirectory creates the entries of a directory containing files and
// symbolic links. Subdirectories can be added using withDirectories().
func newTestDirectory(leaves *testLeaves) []*model_filesystem_pb.DirectoryEntry {
	var entries []*model_filesystem_pb.DirectoryEntry
	for _, file := range leaves.Files {
		entries = append(entries, &model_filesystem_pb.DirectoryEntry{
			Level: &model_filesystem_pb.DirectoryEntry_Node_{
				Node: &model_filesystem_pb.DirectoryEntry_Node{
					Name: file.Name,
					Kind: &model_filesystem_pb.DirectoryEntry_Node_File{File: file.Properties},
				},
			},
		})
	}
	for _, symlink := range leaves.Symlinks {
		entries = append(entries, &model_filesystem_pb.DirectoryEntry{
			Level: &model_filesystem_pb.DirectoryEntry_Node_{
				Node: &model_filesystem_pb.DirectoryEntry_Node{
					Name: symlink.Name,
					Kind: &model_filesystem_pb.DirectoryEntry_Node_Symlink{
						Symlink: &model_filesystem_pb.SymlinkProperties{Target: symlink.Target},
					},
				},
			},
		})
	}
	return sortTestDirectory(entries, nil)
}

// sortTestDirectory constructs the entries of a directory from a list of
// nodes and the contents of the subdirectories, keyed by name. Nodes
// are sorted by name, and are preceded by the contents of the
// subdirectories in the same order.
func sortTestDirectory(nodes []*model_filesystem_pb.DirectoryEntry, contents map[string][]*model_filesystem_pb.DirectoryEntry) []*model_filesystem_pb.DirectoryEntry {
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].GetNode().Name < nodes[j].GetNode().Name
	})
	var entries []*model_filesystem_pb.DirectoryEntry
	for _, node := range nodes {
		if node.GetNode().GetDirectory() != nil {
			entries = append(entries, &model_filesystem_pb.DirectoryEntry{
				Level: &model_filesystem_pb.DirectoryEntry_DirectoryContents_{
					DirectoryContents: &model_filesystem_pb.DirectoryEntry_DirectoryContents{
						Entries: contents[node.GetNode().Name],
					},
				},
			})
		}
	}
	return append(entries, nodes...)
}

// testDirectoryDirectoriesCount returns the number of subdirectories
// of a directory constructed using newTestDirectory().
func testDirectoryDirectoriesCount(directory []*model_filesystem_pb.DirectoryEntry) uint32 {
	count := uint32(0)
	for _, entry := range directory {
		if entry.GetNode().GetDirectory() != nil {
			count++
		}
	}
	return count
}

// testDirectoryMarshaler marshals a directory into an object that can
// be referenced by a DirectoryReference.
func testDirectoryMarshaler(directory []*model_filesystem_pb.DirectoryEntry) encoding.BinaryMarshaler {
	return model_format.NewProtoList[model_filesystem_pb.DirectoryEntry]().NewMarshaler(directory)
}

// testDirectoryNode is a child directory that can be added to a
// directory using withDirectories().
type testDirectoryNode struct {
	name      string
	directory []*model_filesystem_pb.DirectoryEntry
}

func directoryNode(name string, directory []*model_filesystem_pb.DirectoryEntry) testDirectoryNode {
	return testDirectoryNode{
		name:      name,
		directory: directory,
	}
}

// withDirectories adds child directories to a directory. The
// contents of all child directories are stored inline.
func withDirectories(directory []*model_filesystem_pb.DirectoryEntry, children ...testDirectoryNode) []*model_filesystem_pb.DirectoryEntry {
	var nodes []*model_filesystem_pb.DirectoryEntry
	var existingContents [][]*model_filesystem_pb.DirectoryEntry
	for _, entry := range directory {
		if node := entry.GetNode(); node != nil {
			nodes = append(nodes, entry)
		} else {
			existingContents = append(existingContents, entry.GetDirectoryContents().GetEntries())
		}
	}
	contents := map[string][]*model_filesystem_pb.DirectoryEntry{}
	directoryIndex := 0
	for _, node := range nodes {
		if node.GetNode().GetDirectory() != nil {
			contents[node.GetNode().Name] = existingContents[directoryIndex]
			directoryIndex++
		}
	}
	for _, child := range children {
		nodes = append(nodes, &model_filesystem_pb.DirectoryEntry{
			Level: &model_filesystem_pb.DirectoryEntry_Node_{
				Node: &model_filesystem_pb.DirectoryEntry_Node{
					Name: child.name,
					Kind: &model_filesystem_pb.DirectoryEntry_Node_Directory{
						Directory: &model_filesystem_pb.DirectoryProperties{
							DirectoriesCount: testDirectoryDirectoriesCount(child.directory),
						},
					},
				},
			},
		})
		contents[child.name] = child.directory
	}
	return sortTestDirectory(nodes, contents)
}

// singleChildDirectoryContents can be used to construct a directory
// that only contains a single child that is also a directory.
func singleChildDirectoryContents(name string, childContents []*model_filesystem_pb.DirectoryEntry) []*model_filesystem_pb.DirectoryEntry {
	return withDirectories(newTestDirectory(emptyLeaves), directoryNode(name, childContents))
}

func TestFileRoot(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	exampleConfiguration := newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
		return model_format.NewProtoList[model_analysis_pb.BuildSettingOverride]().NewMarshaler([]*model_analysis_pb.BuildSettingOverride{{
			Level: &model_analysis_pb.BuildSettingOverride_Leaf_{
				Leaf: &model_analysis_pb.BuildSettingOverride_Leaf{
					Label: "@@bazel_tools+//command_line_option:platforms",
					Value: &model_starlark_pb.Value{
						Kind: &model_starlark_pb.Value_List{
							List: &model_starlark_pb.List{
								Elements: []*model_starlark_pb.List_Element{{
									Level: &model_starlark_pb.List_Element_Leaf{
										Leaf: &model_starlark_pb.Value{
											Kind: &model_starlark_pb.Value_Label{
												Label: "@@platforms+//host",
											},
										},
									},
								}},
							},
						},
					},
				},
			},
		}})
	})

	t.Run("MissingFile", func(t *testing.T) {
		// Request needs to contain a Starlark File object.
		e := NewMockFileRootEnvironmentForTesting(ctrl)

		_, err := bct.computer.ComputeFileRootValue(
			ctx,
			model_core.NewSimpleMessage[model_core.CreatedObjectTree](
				&model_analysis_pb.FileRoot_Key{
					DirectoryLayout: model_analysis_pb.DirectoryLayout_INPUT_ROOT,
				},
			),
			e,
		)
		require.EqualError(t, err, "no file provided")
	})

	t.Run("BadLabel", func(t *testing.T) {
		// Label in Starlark File object needs to be well formed.
		e := NewMockFileRootEnvironmentForTesting(ctrl)

		_, err := bct.computer.ComputeFileRootValue(
			ctx,
			model_core.NewSimpleMessage[model_core.CreatedObjectTree](
				&model_analysis_pb.FileRoot_Key{
					DirectoryLayout: model_analysis_pb.DirectoryLayout_INPUT_ROOT,
					File: &model_starlark_pb.File{
						Label: "this is not a valid label",
					},
				},
			),
			e,
		)
		require.ErrorContains(t, err, "invalid file label: ")
	})

	t.Run("SourceFile", func(t *testing.T) {
		t.Run("NonExistent", func(t *testing.T) {
			// Labels that refer to non-existent files
			// should cause the creation of a file root to
			// fail.
			e := NewMockFileRootEnvironmentForTesting(ctrl)
			bct.expectGetDirectoryCreationParametersObject(t, e)
			bct.expectGetDirectoryAccessParametersObject(t, e)
			e.EXPECT().GetRepoValue(
				testutil.EqProto(t, &model_analysis_pb.Repo_Key{
					CanonicalRepo: "myrepo+",
				}),
			).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.Repo_Value {
				return &model_analysis_pb.Repo_Value{
					RootDirectoryReference: &model_filesystem_pb.DirectoryReference{
						Reference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
							return testDirectoryMarshaler(newTestDirectory(&testLeaves{
								Files: []*testFileNode{{
									Name:       "bar",
									Properties: &model_filesystem_pb.FileProperties{},
								}},
							}))
						})),
						DirectoriesCount:               0,
						MaximumSymlinkEscapementLevels: &wrapperspb.UInt32Value{Value: 0},
					},
				}
			}))

			_, err := bct.computer.ComputeFileRootValue(
				ctx,
				model_core.NewSimpleMessage[model_core.CreatedObjectTree](
					&model_analysis_pb.FileRoot_Key{
						DirectoryLayout: model_analysis_pb.DirectoryLayout_INPUT_ROOT,
						File: &model_starlark_pb.File{
							Label: "@@myrepo+//:foo",
						},
					},
				),
				e,
			)
			require.ErrorContains(t, err, "Path does not exist")
		})

		t.Run("SuccessSimple", func(t *testing.T) {
			// Simple scenario in which the path resolves to
			// a regular file. The resulting root should
			// only contain the specified file. Any
			// unrelated files should be removed.
			run := func(t *testing.T, directoryLayout model_analysis_pb.DirectoryLayout) model_analysis.PatchedFileRootValue[model_core.CreatedObjectTree] {
				e := NewMockFileRootEnvironmentForTesting(ctrl)
				bct.expectGetDirectoryCreationParametersObject(t, e)
				bct.expectGetDirectoryAccessParametersObject(t, e)
				e.EXPECT().GetRepoValue(
					testutil.EqProto(t, &model_analysis_pb.Repo_Key{
						CanonicalRepo: "myrepo+",
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.Repo_Value {
					return &model_analysis_pb.Repo_Value{
						RootDirectoryReference: &model_filesystem_pb.DirectoryReference{
							Reference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
								return testDirectoryMarshaler(newTestDirectory(&testLeaves{
									Files: []*testFileNode{
										{
											Name:       "bar",
											Properties: &model_filesystem_pb.FileProperties{},
										},
										{
											Name:       "foo",
											Properties: &model_filesystem_pb.FileProperties{},
										},
									},
								}))
							})),
							DirectoriesCount:               0,
							MaximumSymlinkEscapementLevels: &wrapperspb.UInt32Value{Value: 0},
						},
					}
				}))

				fileRoot, err := bct.computer.ComputeFileRootValue(
					ctx,
					model_core.NewSimpleMessage[model_core.CreatedObjectTree](
						&model_analysis_pb.FileRoot_Key{
							DirectoryLayout: directoryLayout,
							File: &model_starlark_pb.File{
								Label: "@@myrepo+//:bar",
							},
						},
					),
					e,
				)
				require.NoError(t, err)
				return fileRoot
			}

			t.Run("InputRoot", func(t *testing.T) {
				// When source files are placed in input
				// roots, they should be named
				// "external/${repo}/${file}".
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_INPUT_ROOT)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"external",
							singleChildDirectoryContents(
								"myrepo+",
								newTestDirectory(&testLeaves{
									Files: []*testFileNode{
										{
											Name:       "bar",
											Properties: &model_filesystem_pb.FileProperties{},
										},
									},
								}),
							),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})

			t.Run("Runfiles", func(t *testing.T) {
				// When source files are placed in
				// runfiles directories, they should be
				// named "${repo}/${file}".
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_RUNFILES)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"myrepo+",
							newTestDirectory(&testLeaves{
								Files: []*testFileNode{
									{
										Name:       "bar",
										Properties: &model_filesystem_pb.FileProperties{},
									},
								},
							}),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})
		})

		t.Run("SuccessComplexDirectory", func(t *testing.T) {
			// Bazel also allows source files to refer to
			// directories. In older versions of Bazel this
			// was unsound, but in recent versions they have
			// made various improvements.
			//
			// These directories may contain symbolic links,
			// which may escape the containing directory or
			// even repository root directory. Referencing
			// files in other repos should cause them to be
			// loaded as well.
			run := func(t *testing.T, directoryLayout model_analysis_pb.DirectoryLayout) model_analysis.PatchedFileRootValue[model_core.CreatedObjectTree] {
				e := NewMockFileRootEnvironmentForTesting(ctrl)
				bct.expectGetDirectoryCreationParametersObject(t, e)
				bct.expectGetDirectoryAccessParametersObject(t, e)
				e.EXPECT().GetRepoValue(
					testutil.EqProto(t, &model_analysis_pb.Repo_Key{
						CanonicalRepo: "myrepo+",
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.Repo_Value {
					return &model_analysis_pb.Repo_Value{
						RootDirectoryReference: &model_filesystem_pb.DirectoryReference{
							Reference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
								return testDirectoryMarshaler(withDirectories(newTestDirectory(&testLeaves{
									Files: []*testFileNode{
										{Name: "file1", Properties: &model_filesystem_pb.FileProperties{}},
										{Name: "file2", Properties: &model_filesystem_pb.FileProperties{}},
										{Name: "file3", Properties: &model_filesystem_pb.FileProperties{}},
										{Name: "file4", Properties: &model_filesystem_pb.FileProperties{}},
									},
								}), directoryNode("dir1", withDirectories(newTestDirectory(&testLeaves{
									Symlinks: []*testSymlinkNode{
										{Name: "dir2", Target: "../dir2"},
										{Name: "file1", Target: "../file1"},
										{Name: "file5", Target: "../../otherrepo+/file5"},
									},
								}), directoryNode("nested", newTestDirectory(&testLeaves{
									Symlinks: []*testSymlinkNode{
										{Name: "file3", Target: "../../file3"},
									},
								})),
								// TODO: Directories belonging to other packages should
								// likely be removed.
								/*
									directoryNode("other_package", newTestDirectory(&testLeaves{
												Files: []*testFileNode{
													{Name: "BUILD.bazel", Properties: &model_filesystem_pb.FileProperties{}},
													{Name: "foo", Properties: &model_filesystem_pb.FileProperties{}},
												},
											})),
								*/)),
									directoryNode("dir2", newTestDirectory(&testLeaves{
										Symlinks: []*testSymlinkNode{
											{Name: "file2", Target: "../file2"},
											{Name: "self", Target: "."},
										},
									}))))
							})),
							DirectoriesCount:               0,
							MaximumSymlinkEscapementLevels: &wrapperspb.UInt32Value{Value: 0},
						},
					}
				}))
				e.EXPECT().GetRepoValue(
					testutil.EqProto(t, &model_analysis_pb.Repo_Key{
						CanonicalRepo: "otherrepo+",
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.Repo_Value {
					return &model_analysis_pb.Repo_Value{
						RootDirectoryReference: &model_filesystem_pb.DirectoryReference{
							Reference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
								return testDirectoryMarshaler(newTestDirectory(&testLeaves{
									Files: []*testFileNode{
										{Name: "file5", Properties: &model_filesystem_pb.FileProperties{}},
										{Name: "file6", Properties: &model_filesystem_pb.FileProperties{}},
									},
								}))
							})),
							DirectoriesCount:               0,
							MaximumSymlinkEscapementLevels: &wrapperspb.UInt32Value{Value: 0},
						},
					}
				}))

				fileRoot, err := bct.computer.ComputeFileRootValue(
					ctx,
					model_core.NewSimpleMessage[model_core.CreatedObjectTree](
						&model_analysis_pb.FileRoot_Key{
							DirectoryLayout: directoryLayout,
							File: &model_starlark_pb.File{
								Label: "@@myrepo+//:dir1",
							},
						},
					),
					e,
				)
				require.NoError(t, err)
				return fileRoot
			}

			t.Run("InputRoot", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_INPUT_ROOT)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"external",
							withDirectories(newTestDirectory(emptyLeaves), directoryNode("myrepo+", withDirectories(newTestDirectory(&testLeaves{
								Files: []*testFileNode{
									{Name: "file1", Properties: &model_filesystem_pb.FileProperties{}},
									{Name: "file2", Properties: &model_filesystem_pb.FileProperties{}},
									{Name: "file3", Properties: &model_filesystem_pb.FileProperties{}},
								},
							}), directoryNode("dir1", withDirectories(newTestDirectory(&testLeaves{
								Symlinks: []*testSymlinkNode{
									{Name: "dir2", Target: "../dir2"},
									{Name: "file1", Target: "../file1"},
									{Name: "file5", Target: "../../otherrepo+/file5"},
								},
							}), directoryNode("nested", newTestDirectory(&testLeaves{
								Symlinks: []*testSymlinkNode{
									{Name: "file3", Target: "../../file3"},
								},
							})))),
								directoryNode("dir2", newTestDirectory(&testLeaves{
									Symlinks: []*testSymlinkNode{
										{Name: "file2", Target: "../file2"},
										{Name: "self", Target: "."},
									},
								})))),
								directoryNode("otherrepo+", newTestDirectory(&testLeaves{
									Files: []*testFileNode{
										{Name: "file5", Properties: &model_filesystem_pb.FileProperties{}},
									},
								}))),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})

			t.Run("Runfiles", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_RUNFILES)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: withDirectories(newTestDirectory(emptyLeaves), directoryNode("myrepo+", withDirectories(newTestDirectory(&testLeaves{
							Files: []*testFileNode{
								{Name: "file1", Properties: &model_filesystem_pb.FileProperties{}},
								{Name: "file2", Properties: &model_filesystem_pb.FileProperties{}},
								{Name: "file3", Properties: &model_filesystem_pb.FileProperties{}},
							},
						}), directoryNode("dir1", withDirectories(newTestDirectory(&testLeaves{
							Symlinks: []*testSymlinkNode{
								{Name: "dir2", Target: "../dir2"},
								{Name: "file1", Target: "../file1"},
								{Name: "file5", Target: "../../otherrepo+/file5"},
							},
						}), directoryNode("nested", newTestDirectory(&testLeaves{
							Symlinks: []*testSymlinkNode{
								{Name: "file3", Target: "../../file3"},
							},
						})))),
							directoryNode("dir2", newTestDirectory(&testLeaves{
								Symlinks: []*testSymlinkNode{
									{Name: "file2", Target: "../file2"},
									{Name: "self", Target: "."},
								},
							})))),
							directoryNode("otherrepo+", newTestDirectory(&testLeaves{
								Files: []*testFileNode{
									{Name: "file5", Properties: &model_filesystem_pb.FileProperties{}},
								},
							}))),
					}
				}, fileRoot)
				fileRoot.Discard()
			})
		})
	})

	t.Run("Action", func(t *testing.T) {
		// TODO: Test error cases.

		t.Run("SuccessSimpleFile", func(t *testing.T) {
			// Request an output file belonging to an action
			// that yields multiple outputs. The resulting
			// file root should only contain the file that's
			// requested.
			run := func(t *testing.T, directoryLayout model_analysis_pb.DirectoryLayout) model_analysis.PatchedFileRootValue[model_core.CreatedObjectTree] {
				e := NewMockFileRootEnvironmentForTesting(ctrl)
				bct.expectCaptureExistingObject(e).Times(3)
				bct.expectGetDirectoryCreationParametersObject(t, e)
				bct.expectGetDirectoryAccessParametersObject(t, e)
				e.EXPECT().GetTargetOutputValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Key {
						return &model_analysis_pb.TargetOutput_Key{
							Label:               "@@myrepo+//:foo",
							Variant:             &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
							PackageRelativePath: "foo.o",
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Value {
					return &model_analysis_pb.TargetOutput_Value{
						Definition: &model_analysis_pb.TargetOutputDefinition{
							Source: &model_analysis_pb.TargetOutputDefinition_ActionId{
								ActionId: []byte{
									0x7a, 0xb1, 0x08, 0xae, 0x94, 0x2d, 0x7d, 0xab,
									0x16, 0x25, 0xd8, 0xbd, 0xc6, 0xd8, 0xdf, 0x27,
								},
							},
						},
					}
				}))
				e.EXPECT().GetTargetActionResultValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetActionResult_Key {
						return &model_analysis_pb.TargetActionResult_Key{
							Id: &model_analysis_pb.TargetActionId{
								Label:   "@@myrepo+//:foo",
								Variant: &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
								ActionId: []byte{
									0x7a, 0xb1, 0x08, 0xae, 0x94, 0x2d, 0x7d, 0xab,
									0x16, 0x25, 0xd8, 0xbd, 0xc6, 0xd8, 0xdf, 0x27,
								},
							},
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetActionResult_Value {
					return &model_analysis_pb.TargetActionResult_Value{
						OutputRoot: singleChildDirectoryContents(
							"bazel-out",
							singleChildDirectoryContents(
								"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
								singleChildDirectoryContents(
									"bin",
									singleChildDirectoryContents(
										"external", singleChildDirectoryContents(
											"myrepo+",
											newTestDirectory(&testLeaves{
												Files: []*testFileNode{
													{
														Name:       "foo.d",
														Properties: &model_filesystem_pb.FileProperties{},
													},
													{
														Name: "foo.o",
														Properties: &model_filesystem_pb.FileProperties{
															Contents: &model_filesystem_pb.FileContents{
																Level: &model_filesystem_pb.FileContents_ChunkReference{
																	ChunkReference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
																		return model_format.Raw{}.NewMarshaler([]byte("File contents go here"))
																	})),
																},
																TotalSizeBytes: 21,
																Utf8Stat:       1,
															},
														},
													},
												},
											}),
										),
									),
								),
							),
						),
					}
				}))

				fileRoot, err := bct.computer.ComputeFileRootValue(
					ctx,
					newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
						return &model_analysis_pb.FileRoot_Key{
							DirectoryLayout: directoryLayout,
							File: &model_starlark_pb.File{
								Label: "@@myrepo+//:foo.o",
								Owner: &model_starlark_pb.File_Owner{
									Variant:    &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
									TargetName: "foo",
									Type:       model_starlark_pb.File_Owner_FILE,
								},
							},
						}
					}),
					e,
				)
				require.NoError(t, err)
				return fileRoot
			}

			t.Run("InputRoot", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_INPUT_ROOT)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"bazel-out",
							singleChildDirectoryContents(
								"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
								singleChildDirectoryContents(
									"bin",
									singleChildDirectoryContents(
										"external",
										singleChildDirectoryContents(
											"myrepo+",
											newTestDirectory(&testLeaves{
												Files: []*testFileNode{{
													Name: "foo.o",
													Properties: &model_filesystem_pb.FileProperties{
														Contents: &model_filesystem_pb.FileContents{
															Level: &model_filesystem_pb.FileContents_ChunkReference{
																ChunkReference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
																	return model_format.Raw{}.NewMarshaler([]byte("File contents go here"))
																})),
															},
															TotalSizeBytes: 21,
															Utf8Stat:       1,
														},
													},
												}},
											}),
										),
									),
								),
							),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})

			t.Run("Runfiles", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_RUNFILES)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"myrepo+",
							newTestDirectory(&testLeaves{
								Files: []*testFileNode{
									{
										Name: "foo.o",
										Properties: &model_filesystem_pb.FileProperties{
											Contents: &model_filesystem_pb.FileContents{
												Level: &model_filesystem_pb.FileContents_ChunkReference{
													ChunkReference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
														return model_format.Raw{}.NewMarshaler([]byte("File contents go here"))
													})),
												},
												TotalSizeBytes: 21,
												Utf8Stat:       1,
											},
										},
									},
								},
							}),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})
		})

		t.Run("SuccessSimpleDirectory", func(t *testing.T) {
			// Similarly, request an output directory
			// belonging to an action that yields multiple
			// outputs. The resulting file root should only
			// contain the directory that's requested.
			run := func(t *testing.T, directoryLayout model_analysis_pb.DirectoryLayout) model_analysis.PatchedFileRootValue[model_core.CreatedObjectTree] {
				e := NewMockFileRootEnvironmentForTesting(ctrl)
				bct.expectCaptureExistingObject(e).Times(2)
				bct.expectGetDirectoryCreationParametersObject(t, e)
				bct.expectGetDirectoryAccessParametersObject(t, e)
				e.EXPECT().GetTargetOutputValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Key {
						return &model_analysis_pb.TargetOutput_Key{
							Label:               "@@myrepo+//:generate_dir",
							Variant:             &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
							PackageRelativePath: "mydir",
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Value {
					return &model_analysis_pb.TargetOutput_Value{
						Definition: &model_analysis_pb.TargetOutputDefinition{
							Source: &model_analysis_pb.TargetOutputDefinition_ActionId{
								ActionId: []byte{
									0x50, 0x6f, 0x44, 0xb5, 0x80, 0xf9, 0xc0, 0xcc,
									0x53, 0xd4, 0x08, 0xe0, 0x6a, 0x32, 0xde, 0x87,
								},
							},
						},
					}
				}))
				e.EXPECT().GetTargetActionResultValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetActionResult_Key {
						return &model_analysis_pb.TargetActionResult_Key{
							Id: &model_analysis_pb.TargetActionId{
								Label:   "@@myrepo+//:generate_dir",
								Variant: &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
								ActionId: []byte{
									0x50, 0x6f, 0x44, 0xb5, 0x80, 0xf9, 0xc0, 0xcc,
									0x53, 0xd4, 0x08, 0xe0, 0x6a, 0x32, 0xde, 0x87,
								},
							},
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetActionResult_Value {
					return &model_analysis_pb.TargetActionResult_Value{
						OutputRoot: singleChildDirectoryContents(
							"bazel-out",
							singleChildDirectoryContents(
								"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
								singleChildDirectoryContents(
									"bin",
									singleChildDirectoryContents(
										"external", singleChildDirectoryContents(
											"myrepo+",
											withDirectories(newTestDirectory(&testLeaves{
												Files: []*testFileNode{
													{
														Name:       "unrelated_file",
														Properties: &model_filesystem_pb.FileProperties{},
													},
												},
											}), directoryNode(
												"mydir", newTestDirectory(&testLeaves{
													Files: []*testFileNode{
														{
															Name:       "myfile",
															Properties: &model_filesystem_pb.FileProperties{},
														},
													},
												}),
											)),
										),
									),
								),
							),
						),
					}
				}))

				fileRoot, err := bct.computer.ComputeFileRootValue(
					ctx,
					newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
						return &model_analysis_pb.FileRoot_Key{
							DirectoryLayout: directoryLayout,
							File: &model_starlark_pb.File{
								Label: "@@myrepo+//:mydir",
								Owner: &model_starlark_pb.File_Owner{
									Variant:    &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
									TargetName: "generate_dir",
									Type:       model_starlark_pb.File_Owner_DIRECTORY,
								},
							},
						}
					}),
					e,
				)
				require.NoError(t, err)
				return fileRoot
			}

			t.Run("InputRoot", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_INPUT_ROOT)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"bazel-out",
							singleChildDirectoryContents(
								"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
								singleChildDirectoryContents(
									"bin",
									singleChildDirectoryContents(
										"external",
										singleChildDirectoryContents(
											"myrepo+",
											singleChildDirectoryContents(
												"mydir",
												newTestDirectory(&testLeaves{
													Files: []*testFileNode{{
														Name:       "myfile",
														Properties: &model_filesystem_pb.FileProperties{},
													}},
												}),
											),
										),
									),
								),
							),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})

			t.Run("Runfiles", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_RUNFILES)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"myrepo+",
							singleChildDirectoryContents(
								"mydir",
								newTestDirectory(&testLeaves{
									Files: []*testFileNode{
										{
											Name:       "myfile",
											Properties: &model_filesystem_pb.FileProperties{},
										},
									},
								}),
							),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})
		})

		t.Run("SymlinkToOtherOutputFile", func(t *testing.T) {
			// If the path corresponds to a symlink pointing
			// to another output file, then the resulting
			// file root should contain both the symlink and
			// the file.
			run := func(t *testing.T, directoryLayout model_analysis_pb.DirectoryLayout) model_analysis.PatchedFileRootValue[model_core.CreatedObjectTree] {
				e := NewMockFileRootEnvironmentForTesting(ctrl)
				bct.expectCaptureExistingObject(e).Times(2)
				bct.expectGetDirectoryCreationParametersObject(t, e)
				bct.expectGetDirectoryAccessParametersObject(t, e)
				e.EXPECT().GetTargetOutputValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Key {
						return &model_analysis_pb.TargetOutput_Key{
							Label:               "@@myrepo+//:foo",
							Variant:             &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
							PackageRelativePath: "bar",
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Value {
					return &model_analysis_pb.TargetOutput_Value{
						Definition: &model_analysis_pb.TargetOutputDefinition{
							Source: &model_analysis_pb.TargetOutputDefinition_ActionId{
								ActionId: []byte{
									0xbd, 0x2a, 0x68, 0x4f, 0x61, 0xf0, 0xa6, 0xd3,
									0x86, 0xae, 0xf0, 0x7a, 0xb1, 0x05, 0x6c, 0x61,
								},
							},
						},
					}
				}))
				e.EXPECT().GetTargetActionResultValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetActionResult_Key {
						return &model_analysis_pb.TargetActionResult_Key{
							Id: &model_analysis_pb.TargetActionId{
								Label:   "@@myrepo+//:foo",
								Variant: &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
								ActionId: []byte{
									0xbd, 0x2a, 0x68, 0x4f, 0x61, 0xf0, 0xa6, 0xd3,
									0x86, 0xae, 0xf0, 0x7a, 0xb1, 0x05, 0x6c, 0x61,
								},
							},
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetActionResult_Value {
					return &model_analysis_pb.TargetActionResult_Value{
						OutputRoot: singleChildDirectoryContents(
							"bazel-out",
							singleChildDirectoryContents(
								"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
								singleChildDirectoryContents(
									"bin",
									singleChildDirectoryContents(
										"external", singleChildDirectoryContents(
											"myrepo+",
											newTestDirectory(&testLeaves{
												Files: []*testFileNode{{
													Name:       "qux",
													Properties: &model_filesystem_pb.FileProperties{},
												}},
												Symlinks: []*testSymlinkNode{
													{Name: "bar", Target: "baz"},
													{Name: "baz", Target: "qux"},
													{Name: "unrelated", Target: "qux"},
												},
											}),
										),
									),
								),
							),
						),
					}
				}))

				fileRoot, err := bct.computer.ComputeFileRootValue(
					ctx,
					newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
						return &model_analysis_pb.FileRoot_Key{
							DirectoryLayout: directoryLayout,
							File: &model_starlark_pb.File{
								Label: "@@myrepo+//:bar",
								Owner: &model_starlark_pb.File_Owner{
									Variant:    &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
									TargetName: "foo",
									Type:       model_starlark_pb.File_Owner_FILE,
								},
							},
						}
					}),
					e,
				)
				require.NoError(t, err)
				return fileRoot
			}

			t.Run("InputRoot", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_INPUT_ROOT)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"bazel-out",
							singleChildDirectoryContents(
								"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
								singleChildDirectoryContents(
									"bin",
									singleChildDirectoryContents(
										"external",
										singleChildDirectoryContents(
											"myrepo+",
											newTestDirectory(&testLeaves{
												Files: []*testFileNode{{
													Name:       "qux",
													Properties: &model_filesystem_pb.FileProperties{},
												}},
												Symlinks: []*testSymlinkNode{
													{Name: "bar", Target: "baz"},
													{Name: "baz", Target: "qux"},
												},
											}),
										),
									),
								),
							),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})

			t.Run("Runfiles", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_RUNFILES)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"myrepo+",
							newTestDirectory(&testLeaves{
								Files: []*testFileNode{{
									Name:       "qux",
									Properties: &model_filesystem_pb.FileProperties{},
								}},
								Symlinks: []*testSymlinkNode{
									{Name: "bar", Target: "baz"},
									{Name: "baz", Target: "qux"},
								},
							}),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})
		})

		t.Run("SymlinkToOtherOutputToInputToOtherInput", func(t *testing.T) {
			// Actions are permitted to yield symlinks that
			// point to files that were part of their input.
			// This means that if the path resolves to a
			// dangling symlink in the TargetActionResult,
			// resolution should continue inside the
			// TargetActionInputRoot.
			run := func(t *testing.T, directoryLayout model_analysis_pb.DirectoryLayout) model_analysis.PatchedFileRootValue[model_core.CreatedObjectTree] {
				e := NewMockFileRootEnvironmentForTesting(ctrl)
				bct.expectCaptureExistingObject(e).Times(3)
				bct.expectGetDirectoryCreationParametersObject(t, e)
				bct.expectGetDirectoryAccessParametersObject(t, e)
				e.EXPECT().GetTargetOutputValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Key {
						return &model_analysis_pb.TargetOutput_Key{
							Label:               "@@myrepo+//:foo",
							Variant:             &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
							PackageRelativePath: "bar",
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Value {
					return &model_analysis_pb.TargetOutput_Value{
						Definition: &model_analysis_pb.TargetOutputDefinition{
							Source: &model_analysis_pb.TargetOutputDefinition_ActionId{
								ActionId: []byte{
									0xbd, 0x2a, 0x68, 0x4f, 0x61, 0xf0, 0xa6, 0xd3,
									0x86, 0xae, 0xf0, 0x7a, 0xb1, 0x05, 0x6c, 0x61,
								},
							},
						},
					}
				}))
				e.EXPECT().GetTargetActionResultValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetActionResult_Key {
						return &model_analysis_pb.TargetActionResult_Key{
							Id: &model_analysis_pb.TargetActionId{
								Label:   "@@myrepo+//:foo",
								Variant: &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
								ActionId: []byte{
									0xbd, 0x2a, 0x68, 0x4f, 0x61, 0xf0, 0xa6, 0xd3,
									0x86, 0xae, 0xf0, 0x7a, 0xb1, 0x05, 0x6c, 0x61,
								},
							},
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetActionResult_Value {
					return &model_analysis_pb.TargetActionResult_Value{
						OutputRoot: singleChildDirectoryContents(
							"bazel-out",
							singleChildDirectoryContents(
								"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
								singleChildDirectoryContents(
									"bin",
									singleChildDirectoryContents(
										"external", singleChildDirectoryContents(
											"myrepo+",
											newTestDirectory(&testLeaves{
												Symlinks: []*testSymlinkNode{
													{Name: "bar", Target: "baz"},
													{Name: "baz", Target: "qux"},
												},
											}),
										),
									),
								),
							),
						),
					}
				}))
				e.EXPECT().GetTargetActionInputRootValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetActionInputRoot_Key {
						return &model_analysis_pb.TargetActionInputRoot_Key{
							Id: &model_analysis_pb.TargetActionId{
								Label:   "@@myrepo+//:foo",
								Variant: &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
								ActionId: []byte{
									0xbd, 0x2a, 0x68, 0x4f, 0x61, 0xf0, 0xa6, 0xd3,
									0x86, 0xae, 0xf0, 0x7a, 0xb1, 0x05, 0x6c, 0x61,
								},
							},
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetActionInputRoot_Value {
					return &model_analysis_pb.TargetActionInputRoot_Value{
						InputRootReference: &model_filesystem_pb.DirectoryReference{
							Reference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
								return testDirectoryMarshaler(singleChildDirectoryContents(
									"bazel-out",
									singleChildDirectoryContents(
										"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
										singleChildDirectoryContents(
											"bin",
											singleChildDirectoryContents(
												"external", singleChildDirectoryContents(
													"myrepo+",
													newTestDirectory(&testLeaves{
														Files: []*testFileNode{{
															Name:       "quux",
															Properties: &model_filesystem_pb.FileProperties{},
														}},
														Symlinks: []*testSymlinkNode{
															{Name: "qux", Target: "quux"},
														},
													}),
												),
											),
										),
									),
								))
							})),
							DirectoriesCount:               1,
							MaximumSymlinkEscapementLevels: &wrapperspb.UInt32Value{Value: 0},
						},
					}
				}))

				fileRoot, err := bct.computer.ComputeFileRootValue(
					ctx,
					newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
						return &model_analysis_pb.FileRoot_Key{
							DirectoryLayout: directoryLayout,
							File: &model_starlark_pb.File{
								Label: "@@myrepo+//:bar",
								Owner: &model_starlark_pb.File_Owner{
									Variant:    &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
									TargetName: "foo",
									Type:       model_starlark_pb.File_Owner_FILE,
								},
							},
						}
					}),
					e,
				)
				require.NoError(t, err)
				return fileRoot
			}

			t.Run("InputRoot", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_INPUT_ROOT)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"bazel-out",
							singleChildDirectoryContents(
								"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
								singleChildDirectoryContents(
									"bin",
									singleChildDirectoryContents(
										"external",
										singleChildDirectoryContents(
											"myrepo+",
											newTestDirectory(&testLeaves{
												Files: []*testFileNode{{
													Name:       "quux",
													Properties: &model_filesystem_pb.FileProperties{},
												}},
												Symlinks: []*testSymlinkNode{
													{Name: "bar", Target: "baz"},
													{Name: "baz", Target: "qux"},
													{Name: "qux", Target: "quux"},
												},
											}),
										),
									),
								),
							),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})

			t.Run("Runfiles", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_RUNFILES)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"myrepo+",
							newTestDirectory(&testLeaves{
								Files: []*testFileNode{{
									Name:       "quux",
									Properties: &model_filesystem_pb.FileProperties{},
								}},
								Symlinks: []*testSymlinkNode{
									{Name: "bar", Target: "baz"},
									{Name: "baz", Target: "qux"},
									{Name: "qux", Target: "quux"},
								},
							}),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})
		})

		t.Run("SymlinkOutOfDirectory", func(t *testing.T) {
			// Output directories may contain symbolic links
			// that point to locations outside the
			// directory. Any files or directories that are
			// referenced should be part of the resulting
			// file root as well.
			run := func(t *testing.T, directoryLayout model_analysis_pb.DirectoryLayout) model_analysis.PatchedFileRootValue[model_core.CreatedObjectTree] {
				e := NewMockFileRootEnvironmentForTesting(ctrl)
				bct.expectCaptureExistingObject(e).Times(2)
				bct.expectGetDirectoryCreationParametersObject(t, e)
				bct.expectGetDirectoryAccessParametersObject(t, e)
				e.EXPECT().GetTargetOutputValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Key {
						return &model_analysis_pb.TargetOutput_Key{
							Label:               "@@myrepo+//:foo",
							Variant:             &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
							PackageRelativePath: "dir1",
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Value {
					return &model_analysis_pb.TargetOutput_Value{
						Definition: &model_analysis_pb.TargetOutputDefinition{
							Source: &model_analysis_pb.TargetOutputDefinition_ActionId{
								ActionId: []byte{
									0xe6, 0x95, 0x9c, 0xa9, 0xe5, 0x33, 0x68, 0xff,
									0x95, 0xbd, 0x21, 0x56, 0xdb, 0xcc, 0xfd, 0x9a,
								},
							},
						},
					}
				}))
				e.EXPECT().GetTargetActionResultValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetActionResult_Key {
						return &model_analysis_pb.TargetActionResult_Key{
							Id: &model_analysis_pb.TargetActionId{
								Label:   "@@myrepo+//:foo",
								Variant: &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
								ActionId: []byte{
									0xe6, 0x95, 0x9c, 0xa9, 0xe5, 0x33, 0x68, 0xff,
									0x95, 0xbd, 0x21, 0x56, 0xdb, 0xcc, 0xfd, 0x9a,
								},
							},
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetActionResult_Value {
					return &model_analysis_pb.TargetActionResult_Value{
						OutputRoot: singleChildDirectoryContents(
							"bazel-out",
							singleChildDirectoryContents(
								"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
								singleChildDirectoryContents(
									"bin",
									singleChildDirectoryContents(
										"external", singleChildDirectoryContents(
											"myrepo+",
											withDirectories(newTestDirectory(&testLeaves{
												Files: []*testFileNode{
													{Name: "file1", Properties: &model_filesystem_pb.FileProperties{}},
													{Name: "file2", Properties: &model_filesystem_pb.FileProperties{}},
													{Name: "file3", Properties: &model_filesystem_pb.FileProperties{}},
													{Name: "file4", Properties: &model_filesystem_pb.FileProperties{}},
												},
											}), directoryNode("dir1", withDirectories(newTestDirectory(&testLeaves{
												Symlinks: []*testSymlinkNode{
													{Name: "dir2", Target: "../dir2"},
													{Name: "file1", Target: "../file1"},
												},
											}), directoryNode("nested", newTestDirectory(&testLeaves{
												Symlinks: []*testSymlinkNode{
													{Name: "file3", Target: "../../file3"},
												},
											})))),
												directoryNode("dir2", newTestDirectory(&testLeaves{
													Symlinks: []*testSymlinkNode{
														{Name: "file2", Target: "../file2"},
														{Name: "self", Target: "."},
													},
												}))),
										),
									),
								),
							),
						),
					}
				}))

				fileRoot, err := bct.computer.ComputeFileRootValue(
					ctx,
					newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
						return &model_analysis_pb.FileRoot_Key{
							DirectoryLayout: directoryLayout,
							File: &model_starlark_pb.File{
								Label: "@@myrepo+//:dir1",
								Owner: &model_starlark_pb.File_Owner{
									Variant:    &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
									TargetName: "foo",
									Type:       model_starlark_pb.File_Owner_DIRECTORY,
								},
							},
						}
					}),
					e,
				)
				require.NoError(t, err)
				return fileRoot
			}

			t.Run("InputRoot", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_INPUT_ROOT)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"bazel-out",
							singleChildDirectoryContents(
								"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
								singleChildDirectoryContents(
									"bin",
									singleChildDirectoryContents(
										"external", singleChildDirectoryContents(
											"myrepo+",
											withDirectories(newTestDirectory(&testLeaves{
												Files: []*testFileNode{
													{Name: "file1", Properties: &model_filesystem_pb.FileProperties{}},
													{Name: "file2", Properties: &model_filesystem_pb.FileProperties{}},
													{Name: "file3", Properties: &model_filesystem_pb.FileProperties{}},
												},
											}), directoryNode("dir1", withDirectories(newTestDirectory(&testLeaves{
												Symlinks: []*testSymlinkNode{
													{Name: "dir2", Target: "../dir2"},
													{Name: "file1", Target: "../file1"},
												},
											}), directoryNode("nested", newTestDirectory(&testLeaves{
												Symlinks: []*testSymlinkNode{
													{Name: "file3", Target: "../../file3"},
												},
											})))),
												directoryNode("dir2", newTestDirectory(&testLeaves{
													Symlinks: []*testSymlinkNode{
														{Name: "file2", Target: "../file2"},
														{Name: "self", Target: "."},
													},
												}))),
										),
									),
								),
							),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})

			t.Run("Runfiles", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_RUNFILES)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"myrepo+",
							withDirectories(newTestDirectory(&testLeaves{
								Files: []*testFileNode{
									{Name: "file1", Properties: &model_filesystem_pb.FileProperties{}},
									{Name: "file2", Properties: &model_filesystem_pb.FileProperties{}},
									{Name: "file3", Properties: &model_filesystem_pb.FileProperties{}},
								},
							}), directoryNode("dir1", withDirectories(newTestDirectory(&testLeaves{
								Symlinks: []*testSymlinkNode{
									{Name: "dir2", Target: "../dir2"},
									{Name: "file1", Target: "../file1"},
								},
							}), directoryNode("nested", newTestDirectory(&testLeaves{
								Symlinks: []*testSymlinkNode{
									{Name: "file3", Target: "../../file3"},
								},
							})))),
								directoryNode("dir2", newTestDirectory(&testLeaves{
									Symlinks: []*testSymlinkNode{
										{Name: "file2", Target: "../file2"},
										{Name: "self", Target: "."},
									},
								}))),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})
		})
	})

	t.Run("ExpandTemplate", func(t *testing.T) {
		// TODO: Test error cases.

		t.Run("Success", func(t *testing.T) {
			// Simulate the computation of the output of:
			//
			//     output = ctx.actions.declare_file("output")
			//     ctx.actions.expand_template(
			//         template = File("@@myrepo+//:template"),
			//         output = output,
			//         substitutions = {
			//             "{{first_name}}": "Albert",
			//             "{{last_name}}": "Einstein",
			//         },
			//         is_executable = True,
			//     )
			run := func(t *testing.T, directoryLayout model_analysis_pb.DirectoryLayout) model_analysis.PatchedFileRootValue[model_core.CreatedObjectTree] {
				e := NewMockFileRootEnvironmentForTesting(ctrl)
				bct.expectCaptureExistingObject(e)
				bct.expectGetDirectoryCreationParametersObject(t, e)
				bct.expectGetDirectoryAccessParametersObject(t, e)
				bct.expectGetFileCreationParametersObject(t, e)
				bct.expectGetFileAccessParametersObject(t, e)
				e.EXPECT().GetTargetOutputValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Key {
						return &model_analysis_pb.TargetOutput_Key{
							Label:               "@@myrepo+//:generate",
							Variant:             &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
							PackageRelativePath: "output",
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Value {
					return &model_analysis_pb.TargetOutput_Value{
						Definition: &model_analysis_pb.TargetOutputDefinition{
							Source: &model_analysis_pb.TargetOutputDefinition_ExpandTemplate_{
								ExpandTemplate: &model_analysis_pb.TargetOutputDefinition_ExpandTemplate{
									Template: &model_starlark_pb.File{
										Label: "@@myrepo+//:template",
									},
									IsExecutable: true,
									Substitutions: []*model_analysis_pb.TargetOutputDefinition_ExpandTemplate_Substitution{
										{Needle: []byte("{{first_name}}"), Replacement: []byte("Albert")},
										{Needle: []byte("{{last_name}}"), Replacement: []byte("Einstein")},
									},
								},
							},
						},
					}
				}))
				e.EXPECT().GetFileRootValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
						return &model_analysis_pb.FileRoot_Key{
							DirectoryLayout: model_analysis_pb.DirectoryLayout_INPUT_ROOT,
							File: &model_starlark_pb.File{
								Label: "@@myrepo+//:template",
							},
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"external",
							singleChildDirectoryContents(
								"myrepo+",
								newTestDirectory(&testLeaves{
									Files: []*testFileNode{
										{
											Name: "template",
											Properties: &model_filesystem_pb.FileProperties{
												Contents: &model_filesystem_pb.FileContents{
													Level: &model_filesystem_pb.FileContents_ChunkReference{
														ChunkReference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
															return model_format.Raw{}.NewMarshaler([]byte("{{first_name}} {{last_name}}"))
														})),
													},
													TotalSizeBytes: 28,
													Utf8Stat:       1,
												},
											},
										},
									},
								}),
							),
						),
					}
				}))
				bct.expectCaptureCreatedObject(e)

				fileRoot, err := bct.computer.ComputeFileRootValue(
					ctx,
					newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
						return &model_analysis_pb.FileRoot_Key{
							DirectoryLayout: directoryLayout,
							File: &model_starlark_pb.File{
								Label: "@@myrepo+//:output",
								Owner: &model_starlark_pb.File_Owner{
									Variant:    &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
									TargetName: "generate",
									Type:       model_starlark_pb.File_Owner_FILE,
								},
							},
						}
					}),
					e,
				)
				require.NoError(t, err)
				return fileRoot
			}

			t.Run("InputRoot", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_INPUT_ROOT)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"bazel-out",
							singleChildDirectoryContents(
								"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
								singleChildDirectoryContents(
									"bin",
									singleChildDirectoryContents(
										"external",
										singleChildDirectoryContents(
											"myrepo+",
											newTestDirectory(&testLeaves{
												Files: []*testFileNode{
													{
														Name: "output",
														Properties: &model_filesystem_pb.FileProperties{
															Contents: &model_filesystem_pb.FileContents{
																Level: &model_filesystem_pb.FileContents_ChunkReference{
																	ChunkReference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
																		return model_format.Raw{}.NewMarshaler([]byte("Albert Einstein"))
																	})),
																},
																TotalSizeBytes: 15,
																Utf8Stat:       1,
															},
															IsExecutable: true,
														},
													},
												},
											}),
										),
									),
								),
							),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})

			t.Run("Runfiles", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_RUNFILES)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"myrepo+",
							newTestDirectory(&testLeaves{
								Files: []*testFileNode{
									{
										Name: "output",
										Properties: &model_filesystem_pb.FileProperties{
											Contents: &model_filesystem_pb.FileContents{
												Level: &model_filesystem_pb.FileContents_ChunkReference{
													ChunkReference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
														return model_format.Raw{}.NewMarshaler([]byte("Albert Einstein"))
													})),
												},
												TotalSizeBytes: 15,
												Utf8Stat:       1,
											},
											IsExecutable: true,
										},
									},
								},
							}),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})
		})
	})

	t.Run("StaticPackageDirectory", func(t *testing.T) {
		// TODO: Test error cases.

		t.Run("Success", func(t *testing.T) {
			// Simulate the computation of the output of:
			//
			//     output = ctx.actions.declare_symlink("passwd")
			//     ctx.actions.symlink(
			//         output = output,
			//         target_path = "/etc/passwd",
			//     )
			run := func(t *testing.T, directoryLayout model_analysis_pb.DirectoryLayout) model_analysis.PatchedFileRootValue[model_core.CreatedObjectTree] {
				e := NewMockFileRootEnvironmentForTesting(ctrl)
				bct.expectCaptureExistingObject(e)
				bct.expectGetDirectoryCreationParametersObject(t, e)
				e.EXPECT().GetTargetOutputValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Key {
						return &model_analysis_pb.TargetOutput_Key{
							Label:               "@@myrepo+//:create_symlink",
							Variant:             &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
							PackageRelativePath: "passwd",
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Value {
					return &model_analysis_pb.TargetOutput_Value{
						Definition: &model_analysis_pb.TargetOutputDefinition{
							Source: &model_analysis_pb.TargetOutputDefinition_StaticPackageDirectory_{
								StaticPackageDirectory: &model_analysis_pb.TargetOutputDefinition_StaticPackageDirectory{
									Entries: newTestDirectory(&testLeaves{
										Symlinks: []*testSymlinkNode{
											{
												Name:   "passwd",
												Target: "/etc/passwd",
											},
										},
									}),
								},
							},
						},
					}
				}))

				fileRoot, err := bct.computer.ComputeFileRootValue(
					ctx,
					newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
						return &model_analysis_pb.FileRoot_Key{
							DirectoryLayout: directoryLayout,
							File: &model_starlark_pb.File{
								Label: "@@myrepo+//:passwd",
								Owner: &model_starlark_pb.File_Owner{
									Variant:    &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
									TargetName: "create_symlink",
									Type:       model_starlark_pb.File_Owner_FILE,
								},
							},
						}
					}),
					e,
				)
				require.NoError(t, err)
				return fileRoot
			}

			t.Run("InputRoot", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_INPUT_ROOT)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"bazel-out",
							singleChildDirectoryContents(
								"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
								singleChildDirectoryContents(
									"bin",
									singleChildDirectoryContents(
										"external",
										singleChildDirectoryContents(
											"myrepo+",
											newTestDirectory(&testLeaves{
												Symlinks: []*testSymlinkNode{
													{
														Name:   "passwd",
														Target: "/etc/passwd",
													},
												},
											}),
										),
									),
								),
							),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})

			t.Run("Runfiles", func(t *testing.T) {
				fileRoot := run(t, model_analysis_pb.DirectoryLayout_RUNFILES)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"myrepo+",
							newTestDirectory(&testLeaves{
								Symlinks: []*testSymlinkNode{
									{
										Name:   "passwd",
										Target: "/etc/passwd",
									},
								},
							}),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})
		})
	})

	t.Run("Symlink", func(t *testing.T) {
		// TODO: Test error cases.

		t.Run("FileWhileDirectoryWasExpected", func(t *testing.T) {
			// If ctx.actions.symlink() is provided an
			// output of type directory, then the target
			// should resolve to a directory as well.
			e := NewMockFileRootEnvironmentForTesting(ctrl)
			bct.expectCaptureExistingObject(e)
			bct.expectGetDirectoryCreationParametersObject(t, e)
			bct.expectGetDirectoryAccessParametersObject(t, e)
			e.EXPECT().GetTargetOutputValue(
				eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Key {
					return &model_analysis_pb.TargetOutput_Key{
						Label:               "@@myrepo+//:create_symlink",
						Variant:             &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
						PackageRelativePath: "b",
					}
				}),
			).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Value {
				return &model_analysis_pb.TargetOutput_Value{
					Definition: &model_analysis_pb.TargetOutputDefinition{
						Source: &model_analysis_pb.TargetOutputDefinition_Symlink_{
							Symlink: &model_analysis_pb.TargetOutputDefinition_Symlink{
								Target: &model_starlark_pb.File{
									Label: "@@myrepo+//:a",
								},
							},
						},
					},
				}
			}))
			e.EXPECT().GetFileRootValue(
				eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
					return &model_analysis_pb.FileRoot_Key{
						DirectoryLayout: model_analysis_pb.DirectoryLayout_INPUT_ROOT,
						File: &model_starlark_pb.File{
							Label: "@@myrepo+//:a",
						},
					}
				}),
			).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
				return &model_analysis_pb.FileRoot_Value{
					RootDirectory: singleChildDirectoryContents(
						"external",
						singleChildDirectoryContents(
							"myrepo+",
							newTestDirectory(&testLeaves{
								Files: []*testFileNode{
									{
										Name:       "a",
										Properties: &model_filesystem_pb.FileProperties{},
									},
								},
							}),
						),
					),
				}
			}))

			_, err := bct.computer.ComputeFileRootValue(
				ctx,
				newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
					return &model_analysis_pb.FileRoot_Key{
						DirectoryLayout: model_analysis_pb.DirectoryLayout_INPUT_ROOT,
						File: &model_starlark_pb.File{
							Label: "@@myrepo+//:b",
							Owner: &model_starlark_pb.File_Owner{
								Variant:    &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
								TargetName: "create_symlink",
								Type:       model_starlark_pb.File_Owner_DIRECTORY,
							},
						},
					}
				}),
				e,
			)
			require.EqualError(t, err, "path \"external/myrepo+/a\" resolves to a file, while a directory was expected")
		})

		t.Run("DirectoryWhileFileWasExpected", func(t *testing.T) {
			// If ctx.actions.symlink() is provided an
			// output of type file, then the target should
			// resolve to a file as well.
			e := NewMockFileRootEnvironmentForTesting(ctrl)
			bct.expectCaptureExistingObject(e)
			bct.expectGetDirectoryCreationParametersObject(t, e)
			bct.expectGetDirectoryAccessParametersObject(t, e)
			e.EXPECT().GetTargetOutputValue(
				eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Key {
					return &model_analysis_pb.TargetOutput_Key{
						Label:               "@@myrepo+//:create_symlink",
						Variant:             &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
						PackageRelativePath: "b",
					}
				}),
			).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Value {
				return &model_analysis_pb.TargetOutput_Value{
					Definition: &model_analysis_pb.TargetOutputDefinition{
						Source: &model_analysis_pb.TargetOutputDefinition_Symlink_{
							Symlink: &model_analysis_pb.TargetOutputDefinition_Symlink{
								Target: &model_starlark_pb.File{
									Label: "@@myrepo+//:a",
								},
							},
						},
					},
				}
			}))
			e.EXPECT().GetFileRootValue(
				eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
					return &model_analysis_pb.FileRoot_Key{
						DirectoryLayout: model_analysis_pb.DirectoryLayout_INPUT_ROOT,
						File: &model_starlark_pb.File{
							Label: "@@myrepo+//:a",
						},
					}
				}),
			).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
				return &model_analysis_pb.FileRoot_Value{
					RootDirectory: singleChildDirectoryContents(
						"external",
						singleChildDirectoryContents(
							"myrepo+",
							singleChildDirectoryContents(
								"a",
								newTestDirectory(emptyLeaves),
							),
						),
					),
				}
			}))

			_, err := bct.computer.ComputeFileRootValue(
				ctx,
				newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
					return &model_analysis_pb.FileRoot_Key{
						DirectoryLayout: model_analysis_pb.DirectoryLayout_INPUT_ROOT,
						File: &model_starlark_pb.File{
							Label: "@@myrepo+//:b",
							Owner: &model_starlark_pb.File_Owner{
								Variant:    &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
								TargetName: "create_symlink",
								Type:       model_starlark_pb.File_Owner_FILE,
							},
						},
					}
				}),
				e,
			)
			require.EqualError(t, err, "path \"external/myrepo+/a\" resolves to a directory, while a file was expected")
		})

		t.Run("FileMissingIsExecutable", func(t *testing.T) {
			// If ctx.actions.symlink() is called with
			// is_executable=True, then the target should
			// resolve to a file that is executable as well.
			e := NewMockFileRootEnvironmentForTesting(ctrl)
			bct.expectCaptureExistingObject(e)
			bct.expectGetDirectoryCreationParametersObject(t, e)
			bct.expectGetDirectoryAccessParametersObject(t, e)
			e.EXPECT().GetTargetOutputValue(
				eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Key {
					return &model_analysis_pb.TargetOutput_Key{
						Label:               "@@myrepo+//:create_symlink",
						Variant:             &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
						PackageRelativePath: "b",
					}
				}),
			).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Value {
				return &model_analysis_pb.TargetOutput_Value{
					Definition: &model_analysis_pb.TargetOutputDefinition{
						Source: &model_analysis_pb.TargetOutputDefinition_Symlink_{
							Symlink: &model_analysis_pb.TargetOutputDefinition_Symlink{
								Target: &model_starlark_pb.File{
									Label: "@@myrepo+//:a",
								},
								IsExecutable: true,
							},
						},
					},
				}
			}))
			e.EXPECT().GetFileRootValue(
				eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
					return &model_analysis_pb.FileRoot_Key{
						DirectoryLayout: model_analysis_pb.DirectoryLayout_INPUT_ROOT,
						File: &model_starlark_pb.File{
							Label: "@@myrepo+//:a",
						},
					}
				}),
			).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
				return &model_analysis_pb.FileRoot_Value{
					RootDirectory: singleChildDirectoryContents(
						"external",
						singleChildDirectoryContents(
							"myrepo+",
							newTestDirectory(&testLeaves{
								Files: []*testFileNode{
									{
										Name:       "a",
										Properties: &model_filesystem_pb.FileProperties{},
									},
								},
							}),
						),
					),
				}
			}))

			_, err := bct.computer.ComputeFileRootValue(
				ctx,
				newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
					return &model_analysis_pb.FileRoot_Key{
						DirectoryLayout: model_analysis_pb.DirectoryLayout_INPUT_ROOT,
						File: &model_starlark_pb.File{
							Label: "@@myrepo+//:b",
							Owner: &model_starlark_pb.File_Owner{
								Variant:    &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
								TargetName: "create_symlink",
								Type:       model_starlark_pb.File_Owner_FILE,
							},
						},
					}
				}),
				e,
			)
			require.EqualError(t, err, "file at path \"external/myrepo+/a\" is not executable, even though it should be")
		})

		t.Run("Success", func(t *testing.T) {
			// Simulate the computation of the output of:
			//
			//     output = ctx.actions.declare_file("b")
			//     ctx.actions.symlink(
			//         output = output,
			//         target_file = File("@@myrepo+//:a"),
			//     )
			run := func(t *testing.T, directoryLayout model_analysis_pb.DirectoryLayout, target []*model_filesystem_pb.DirectoryEntry) model_analysis.PatchedFileRootValue[model_core.CreatedObjectTree] {
				e := NewMockFileRootEnvironmentForTesting(ctrl)
				bct.expectCaptureExistingObject(e)
				bct.expectGetDirectoryCreationParametersObject(t, e)
				bct.expectGetDirectoryAccessParametersObject(t, e)
				e.EXPECT().GetTargetOutputValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Key {
						return &model_analysis_pb.TargetOutput_Key{
							Label:               "@@myrepo+//:create_symlink",
							Variant:             &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
							PackageRelativePath: "b",
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetOutput_Value {
					return &model_analysis_pb.TargetOutput_Value{
						Definition: &model_analysis_pb.TargetOutputDefinition{
							Source: &model_analysis_pb.TargetOutputDefinition_Symlink_{
								Symlink: &model_analysis_pb.TargetOutputDefinition_Symlink{
									Target: &model_starlark_pb.File{
										Label: "@@myrepo+//:a",
									},
								},
							},
						},
					}
				}))
				e.EXPECT().GetFileRootValue(
					eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
						return &model_analysis_pb.FileRoot_Key{
							DirectoryLayout: directoryLayout,
							File: &model_starlark_pb.File{
								Label: "@@myrepo+//:a",
							},
						}
					}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: target,
					}
				}))

				fileRoot, err := bct.computer.ComputeFileRootValue(
					ctx,
					newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Key {
						return &model_analysis_pb.FileRoot_Key{
							DirectoryLayout: directoryLayout,
							File: &model_starlark_pb.File{
								Label: "@@myrepo+//:b",
								Owner: &model_starlark_pb.File_Owner{
									Variant:    &model_starlark_pb.TargetVariant{ConfigurationReference: attachObject(patcher, exampleConfiguration)},
									TargetName: "create_symlink",
									Type:       model_starlark_pb.File_Owner_FILE,
								},
							},
						}
					}),
					e,
				)
				require.NoError(t, err)
				return fileRoot
			}

			t.Run("InputRoot", func(t *testing.T) {
				fileRoot := run(
					t,
					model_analysis_pb.DirectoryLayout_INPUT_ROOT,
					singleChildDirectoryContents(
						"external",
						singleChildDirectoryContents(
							"myrepo+",
							newTestDirectory(&testLeaves{
								Files: []*testFileNode{
									{
										Name:       "a",
										Properties: &model_filesystem_pb.FileProperties{},
									},
								},
							}),
						),
					),
				)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: withDirectories(newTestDirectory(emptyLeaves), directoryNode("bazel-out", singleChildDirectoryContents(
							"Cg6Kx80o8BPYmGdgWYfRZvbKyWojQ7snQzHOx70XAwRPAAAAAAAAAA.",
							singleChildDirectoryContents(
								"bin",
								singleChildDirectoryContents(
									"external",
									singleChildDirectoryContents(
										"myrepo+",
										newTestDirectory(&testLeaves{
											Symlinks: []*testSymlinkNode{
												{
													Name:   "b",
													Target: "../../../../../external/myrepo+/a",
												},
											},
										}),
									),
								),
							),
						)),
							directoryNode("external", singleChildDirectoryContents(
								"myrepo+",
								newTestDirectory(&testLeaves{
									Files: []*testFileNode{
										{
											Name:       "a",
											Properties: &model_filesystem_pb.FileProperties{},
										},
									},
								}),
							))),
					}
				}, fileRoot)
				fileRoot.Discard()
			})

			t.Run("Runfiles", func(t *testing.T) {
				fileRoot := run(
					t,
					model_analysis_pb.DirectoryLayout_RUNFILES,
					singleChildDirectoryContents(
						"myrepo+",
						newTestDirectory(&testLeaves{
							Files: []*testFileNode{
								{
									Name:       "a",
									Properties: &model_filesystem_pb.FileProperties{},
								},
							},
						}),
					),
				)
				requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.FileRoot_Value {
					return &model_analysis_pb.FileRoot_Value{
						RootDirectory: singleChildDirectoryContents(
							"myrepo+",
							newTestDirectory(&testLeaves{
								Files: []*testFileNode{
									{
										Name:       "a",
										Properties: &model_filesystem_pb.FileProperties{},
									},
								},
								Symlinks: []*testSymlinkNode{
									{
										Name:   "b",
										Target: "a",
									},
								},
							}),
						),
					}
				}, fileRoot)
				fileRoot.Discard()
			})
		})
	})
}
