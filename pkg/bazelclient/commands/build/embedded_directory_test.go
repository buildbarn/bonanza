package build

import (
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"bonanza.build/pkg/bazelclient/embeddedmodules"
	"bonanza.build/pkg/dotbazel"
	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedDirectory(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		root := &embeddedDirectory[model_core.CreatedObjectTree, model_core.NoopReferenceMetadata]{
			fsys: fstest.MapFS{
				"example/MODULE.bazel":      {Data: []byte("module(name = \"example\")\n")},
				"example/BUILD.tools":       {Data: []byte("# Root package\n")},
				"example/tools/BUILD.tools": {Data: []byte("")},
				"example/tools/run.sh":      {Data: []byte("#!/bin/sh\n")},
			},
			path: (*path.Trace)(nil).Append(path.MustNewComponent("example")),
		}

		// BUILD.tools files should be renamed to BUILD.bazel.
		// Files starting with a shebang should be executable.
		entries, err := root.ReadDir()
		require.NoError(t, err)
		require.Equal(t, []filesystem.FileInfo{
			filesystem.NewFileInfo(path.MustNewComponent("BUILD.bazel"), filesystem.FileTypeRegularFile, false),
			filesystem.NewFileInfo(path.MustNewComponent("MODULE.bazel"), filesystem.FileTypeRegularFile, false),
			filesystem.NewFileInfo(path.MustNewComponent("tools"), filesystem.FileTypeDirectory, false),
		}, entries)

		_, child, err := root.EnterCapturableDirectory(path.MustNewComponent("tools"))
		require.NoError(t, err)
		entries, err = child.ReadDir()
		require.NoError(t, err)
		require.Equal(t, []filesystem.FileInfo{
			filesystem.NewFileInfo(path.MustNewComponent("BUILD.bazel"), filesystem.FileTypeRegularFile, false),
			filesystem.NewFileInfo(path.MustNewComponent("run.sh"), filesystem.FileTypeRegularFile, true),
		}, entries)

		_, _, err = root.EnterCapturableDirectory(path.MustNewComponent("MODULE.bazel"))
		require.Error(t, err)

		// The same directory should also be usable as a
		// CapturedDirectory for uploading.
		var captured model_filesystem.CapturedDirectory = root

		// BUILD.bazel should be readable, while BUILD.tools
		// should not be accessible under its original name.
		f, err := captured.OpenRead(path.MustNewComponent("BUILD.bazel"))
		require.NoError(t, err)
		data, err := io.ReadAll(io.NewSectionReader(f, 0, 1000))
		require.NoError(t, err)
		require.Equal(t, "# Root package\n", string(data))
		_, err = captured.OpenRead(path.MustNewComponent("BUILD.tools"))
		require.Error(t, err)

		capturedChild, err := captured.EnterCapturedDirectory(path.MustNewComponent("tools"))
		require.NoError(t, err)
		f, err = capturedChild.OpenRead(path.MustNewComponent("run.sh"))
		require.NoError(t, err)
		size, err := f.Len()
		require.NoError(t, err)
		require.Equal(t, int64(10), size)
		offset, err := f.GetNextRegionOffset(3, filesystem.Data)
		require.NoError(t, err)
		require.Equal(t, int64(3), offset)
		offset, err = f.GetNextRegionOffset(3, filesystem.Hole)
		require.NoError(t, err)
		require.Equal(t, int64(10), offset)
		_, err = f.GetNextRegionOffset(10, filesystem.Data)
		require.Equal(t, io.EOF, err)
		require.NoError(t, f.Close())
	})

	t.Run("ShortFiles", func(t *testing.T) {
		// Files that are too short to contain a shebang should
		// not be considered executable.
		root := &embeddedDirectory[model_core.CreatedObjectTree, model_core.NoopReferenceMetadata]{
			fsys: fstest.MapFS{
				"example/empty":     {Data: []byte("")},
				"example/hash":      {Data: []byte("#")},
				"example/shebang":   {Data: []byte("#!")},
				"example/directory": {Mode: fs.ModeDir},
			},
			path: (*path.Trace)(nil).Append(path.MustNewComponent("example")),
		}
		entries, err := root.ReadDir()
		require.NoError(t, err)
		require.Equal(t, []filesystem.FileInfo{
			filesystem.NewFileInfo(path.MustNewComponent("directory"), filesystem.FileTypeDirectory, false),
			filesystem.NewFileInfo(path.MustNewComponent("empty"), filesystem.FileTypeRegularFile, false),
			filesystem.NewFileInfo(path.MustNewComponent("hash"), filesystem.FileTypeRegularFile, false),
			filesystem.NewFileInfo(path.MustNewComponent("shebang"), filesystem.FileTypeRegularFile, true),
		}, entries)

		// Directories cannot be opened as files.
		_, err = root.OpenRead(path.MustNewComponent("directory"))
		require.Error(t, err)
	})

	t.Run("Conflict", func(t *testing.T) {
		// Embedded modules may not contain files named
		// BUILD.bazel, as they would conflict with renamed
		// BUILD.tools files.
		root := &embeddedDirectory[model_core.CreatedObjectTree, model_core.NoopReferenceMetadata]{
			fsys: fstest.MapFS{
				"example/BUILD.bazel": {Data: []byte("")},
			},
			path: (*path.Trace)(nil).Append(path.MustNewComponent("example")),
		}
		_, err := root.ReadDir()
		require.Error(t, err)
	})

	t.Run("EmbeddedModules", func(t *testing.T) {
		// None of the modules embedded into the binary should
		// contain files named BUILD or BUILD.bazel. All of
		// their directories should be listable, which requires
		// that embedded files support random access reads.
		entries, err := fs.ReadDir(embeddedmodules.Modules, ".")
		require.NoError(t, err)
		require.NotEmpty(t, entries)
		require.NoError(t, fs.WalkDir(embeddedmodules.Modules, ".", func(p string, d fs.DirEntry, err error) error {
			require.NoError(t, err)
			require.NotContains(t, []string{dotbazel.BuildFilename, dotbazel.LegacyBuildFilename}, d.Name(), p)
			if d.IsDir() {
				var directoryPath *path.Trace
				if p != "." {
					for _, component := range strings.Split(p, "/") {
						directoryPath = directoryPath.Append(path.MustNewComponent(component))
					}
				}
				directory := &embeddedDirectory[model_core.CreatedObjectTree, model_core.NoopReferenceMetadata]{
					fsys: embeddedmodules.Modules,
					path: directoryPath,
				}
				_, err := directory.ReadDir()
				require.NoError(t, err, p)
			}
			return nil
		}))
	})
}
