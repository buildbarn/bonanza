package analysis_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"math/rand"
	"testing"

	model_analysis "bonanza.build/pkg/model/analysis"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/go-cdc"
	"github.com/stretchr/testify/require"
	"github.com/ulikunitz/xz"

	"go.uber.org/mock/gomock"
)

// testArchiveEntry is an entry that is written into an archive that is
// constructed by newTestTarball() or newTestZIPArchive().
type testArchiveEntry struct {
	name     string
	typeflag byte
	mode     int64
	body     string
	linkname string
}

func newTestTarball(t *testing.T, newCompressor func(io.Writer) io.WriteCloser, entries []testArchiveEntry) []byte {
	var b bytes.Buffer
	compressor := newCompressor(&b)
	tarWriter := tar.NewWriter(compressor)
	for _, entry := range entries {
		require.NoError(t, tarWriter.WriteHeader(&tar.Header{
			Name:     entry.name,
			Typeflag: entry.typeflag,
			Mode:     entry.mode,
			Size:     int64(len(entry.body)),
			Linkname: entry.linkname,
		}))
		_, err := tarWriter.Write([]byte(entry.body))
		require.NoError(t, err)
	}
	require.NoError(t, tarWriter.Close())
	require.NoError(t, compressor.Close())
	return b.Bytes()
}

func newGzipCompressor(w io.Writer) io.WriteCloser {
	return gzip.NewWriter(w)
}

func newXZCompressor(w io.Writer) io.WriteCloser {
	compressor, err := xz.NewWriter(w)
	if err != nil {
		panic(err)
	}
	return compressor
}

func newTestZIPArchive(t *testing.T, entries []testArchiveEntry) []byte {
	var b bytes.Buffer
	zipWriter := zip.NewWriter(&b)
	for _, entry := range entries {
		w, err := zipWriter.Create(entry.name)
		require.NoError(t, err)
		_, err = w.Write([]byte(entry.body))
		require.NoError(t, err)
	}
	require.NoError(t, zipWriter.Close())
	return b.Bytes()
}

// testRandomData returns a deterministic sequence of bytes that cannot
// be compressed. This can be used to create archives that are large
// enough to cause progress to be reported.
func testRandomData(sizeBytes int) string {
	data := make([]byte, sizeBytes)
	rand.New(rand.NewSource(1)).Read(data)
	return string(data)
}

var testFetchOptions = &model_analysis_pb.HttpFetchOptions{
	Target: &model_fetch_pb.Target{
		Urls: []string{"https://example.com/archive"},
	},
}

// httpArchiveContentsResult contains the outcome of calling
// ComputeHTTPArchiveContentsValue().
type httpArchiveContentsResult struct {
	value    model_analysis.PatchedHTTPArchiveContentsValue[model_core.CreatedObjectTree]
	err      error
	progress []*model_analysis_pb.HttpArchiveContents_Progress
}

// computeHTTPArchiveContents calls ComputeHTTPArchiveContentsValue(),
// letting HttpFileContents return the provided archive.
func (bct *baseComputerTester) computeHTTPArchiveContents(ctx context.Context, t *testing.T, ctrl *gomock.Controller, format model_analysis_pb.HttpArchiveContents_Key_Format, archive []byte) httpArchiveContentsResult {
	capturer := createdObjectTreeCapturer{}
	fileCreationParameters := bct.newFileCreationParameters()
	archiveContents, err := model_filesystem.CreateFileMerkleTree(
		ctx,
		fileCreationParameters,
		cdc.NewBytesPeeker(archive),
		model_filesystem.NewSimpleFileMerkleTreeCapturer[model_core.CreatedObjectTree](capturer),
	)
	require.NoError(t, err)
	archiveContentsMessage := model_core.Unpatch(capturer, archiveContents).Decay()

	e := NewMockHTTPArchiveContentsEnvironmentForTesting(ctrl)
	e.EXPECT().GetFileAccessParametersObject(
		testutil.EqProto(t, &model_analysis_pb.FileAccessParameters_Key{}),
	).Return(bct.newFileReader(), true)
	e.EXPECT().GetDirectoryCreationParametersObject(
		testutil.EqProto(t, &model_analysis_pb.DirectoryCreationParameters_Key{}),
	).Return(bct.newDirectoryCreationParameters(), true)
	e.EXPECT().GetFileCreationParametersObject(
		testutil.EqProto(t, &model_analysis_pb.FileCreationParameters_Key{}),
	).Return(bct.newFileCreationParameters(), true)
	e.EXPECT().GetHTTPFileContentsValue(
		testutil.EqProto(t, &model_analysis_pb.HttpFileContents_Key{
			FetchOptions: testFetchOptions,
		}),
	).Return(model_core.Nested(archiveContentsMessage, &model_analysis_pb.HttpFileContents_Value{
		Exists: &model_fetch_pb.Result_Success{
			Contents: archiveContentsMessage.Message,
		},
	}))
	e.EXPECT().CaptureCreatedObject(gomock.Any(), gomock.Any()).
		DoAndReturn(capturer.CaptureCreatedObject).AnyTimes()
	e.EXPECT().CaptureExistingObject(gomock.Any()).
		DoAndReturn(capturer.CaptureExistingObject).AnyTimes()
	e.EXPECT().ReferenceObject(gomock.Any()).
		DoAndReturn(capturer.ReferenceObject).AnyTimes()

	var result httpArchiveContentsResult
	e.EXPECT().ReportHTTPArchiveContentsProgress(gomock.Any()).
		Do(func(progress *model_analysis_pb.HttpArchiveContents_Progress) {
			result.progress = append(result.progress, progress)
		}).AnyTimes()

	result.value, result.err = bct.computer.ComputeHTTPArchiveContentsValue(
		ctx,
		&model_analysis_pb.HttpArchiveContents_Key{
			FetchOptions: testFetchOptions,
			Format:       format,
		},
		e,
	)
	return result
}

// readHTTPArchiveContents flattens the directory hierarchy returned by
// ComputeHTTPArchiveContentsValue(), so that it can be compared
// against expected contents. Directories are suffixed with "/". Files
// map to "file:" or "executable:", followed by their contents. Symbolic
// links map to "symlink:", followed by their target.
func (bct *baseComputerTester) readHTTPArchiveContents(ctx context.Context, t *testing.T, value model_analysis.PatchedHTTPArchiveContentsValue[model_core.CreatedObjectTree]) map[string]string {
	m := model_core.Unpatch(createdObjectTreeCapturer{}, value).Decay()
	require.NotNil(t, m.Message.Exists)

	directoryReader := model_parser.LookupParsedObjectReader(
		bct.parsedObjectPoolIngester,
		model_parser.NewProtoListObjectParser[model_core.CreatedObjectTree, model_filesystem_pb.DirectoryEntry](),
	)
	fileReader := bct.newFileReader()
	contents := map[string]string{}

	var walk func(prefix string, directory model_core.Message[[]*model_filesystem_pb.DirectoryEntry, model_core.CreatedObjectTree])
	walk = func(prefix string, directory model_core.Message[[]*model_filesystem_pb.DirectoryEntry, model_core.CreatedObjectTree]) {
		var errIter error
		for node := range model_filesystem.DirectoryAllNodes(ctx, directoryReader, directory, &errIter) {
			name := prefix + node.Message.Name
			switch kind := node.Message.Kind.(type) {
			case *model_filesystem_pb.DirectoryEntry_Node_Directory:
				contents[name+"/"] = "directory"
			case *model_filesystem_pb.DirectoryEntry_Node_File:
				entry, err := model_filesystem.NewFileContentsEntryFromProto(model_core.Nested(node, kind.File.Contents))
				require.NoError(t, err)
				data, err := fileReader.FileReadAll(ctx, entry, 1<<30)
				require.NoError(t, err)
				if kind.File.IsExecutable {
					contents[name] = "executable:" + string(data)
				} else {
					contents[name] = "file:" + string(data)
				}
			case *model_filesystem_pb.DirectoryEntry_Node_Symlink:
				contents[name] = "symlink:" + kind.Symlink.Target
			default:
				t.Fatalf("Unknown kind of node %#v", name)
			}
		}
		require.NoError(t, errIter)

		for node, childDirectory := range model_filesystem.DirectoryAllChildren(ctx, directoryReader, directory, &errIter) {
			walk(prefix+node.Message.Name+"/", childDirectory)
		}
		require.NoError(t, errIter)
	}

	rootDirectory, err := model_filesystem.DirectoryDereference(ctx, directoryReader, model_core.Nested(m, m.Message.Exists.Contents))
	require.NoError(t, err)
	walk("", rootDirectory)
	return contents
}

func TestHTTPArchiveContents(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	t.Run("MissingDependencies", func(t *testing.T) {
		// If the archive has not been downloaded yet, the
		// evaluation function should request it.
		e := NewMockHTTPArchiveContentsEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(bct.newFileReader(), true)
		e.EXPECT().GetDirectoryCreationParametersObject(gomock.Any()).Return(bct.newDirectoryCreationParameters(), true)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(bct.newFileCreationParameters(), true)
		e.EXPECT().GetHTTPFileContentsValue(
			testutil.EqProto(t, &model_analysis_pb.HttpFileContents_Key{
				FetchOptions: testFetchOptions,
			}),
		).Return(model_core.Message[*model_analysis_pb.HttpFileContents_Value, model_core.CreatedObjectTree]{})

		_, err := bct.computer.ComputeHTTPArchiveContentsValue(
			ctx,
			&model_analysis_pb.HttpArchiveContents_Key{
				FetchOptions: testFetchOptions,
				Format:       model_analysis_pb.HttpArchiveContents_Key_TAR_GZ,
			},
			e,
		)
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})

	t.Run("FileDoesNotExist", func(t *testing.T) {
		// If the server returned a non-200 status code, there is
		// no archive to extract.
		e := NewMockHTTPArchiveContentsEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(bct.newFileReader(), true)
		e.EXPECT().GetDirectoryCreationParametersObject(gomock.Any()).Return(bct.newDirectoryCreationParameters(), true)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(bct.newFileCreationParameters(), true)
		e.EXPECT().GetHTTPFileContentsValue(gomock.Any()).
			Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.HttpFileContents_Value{}))

		_, err := bct.computer.ComputeHTTPArchiveContentsValue(
			ctx,
			&model_analysis_pb.HttpArchiveContents_Key{
				FetchOptions: testFetchOptions,
				Format:       model_analysis_pb.HttpArchiveContents_Key_TAR_GZ,
			},
			e,
		)
		require.EqualError(t, err, "file does not exist")
	})

	t.Run("UnknownFormat", func(t *testing.T) {
		result := bct.computeHTTPArchiveContents(ctx, t, ctrl, model_analysis_pb.HttpArchiveContents_Key_UNKNOWN, []byte("Hello"))
		require.EqualError(t, result.err, "unknown archive format")
	})

	// Entries of a tarball that exercise all supported file types.
	tarballEntries := []testArchiveEntry{
		{name: "dir/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "dir/file.txt", typeflag: tar.TypeReg, mode: 0o644, body: "Hello"},
		{name: "dir/tool.sh", typeflag: tar.TypeReg, mode: 0o755, body: "#!/bin/sh"},
		{name: "dir/empty", typeflag: tar.TypeReg, mode: 0o644},
		{name: "dir/link", typeflag: tar.TypeSymlink, linkname: "file.txt"},
		// Parent directories of files don't need to be
		// present in the archive explicitly.
		{name: "implicit/nested/file.txt", typeflag: tar.TypeReg, mode: 0o644, body: "Nested"},
		// Hard links share the contents and permissions of
		// a file extracted previously.
		{name: "hardlink", typeflag: tar.TypeLink, linkname: "dir/tool.sh"},
	}
	expectedTarballContents := map[string]string{
		"dir/":                     "directory",
		"dir/empty":                "file:",
		"dir/file.txt":             "file:Hello",
		"dir/link":                 "symlink:file.txt",
		"dir/tool.sh":              "executable:#!/bin/sh",
		"hardlink":                 "executable:#!/bin/sh",
		"implicit/":                "directory",
		"implicit/nested/":         "directory",
		"implicit/nested/file.txt": "file:Nested",
	}

	t.Run("TarGz", func(t *testing.T) {
		archive := newTestTarball(t, newGzipCompressor, tarballEntries)
		result := bct.computeHTTPArchiveContents(ctx, t, ctrl, model_analysis_pb.HttpArchiveContents_Key_TAR_GZ, archive)
		require.NoError(t, result.err)
		require.Equal(t, expectedTarballContents, bct.readHTTPArchiveContents(ctx, t, result.value))
	})

	t.Run("TarXz", func(t *testing.T) {
		archive := newTestTarball(t, newXZCompressor, tarballEntries)
		result := bct.computeHTTPArchiveContents(ctx, t, ctrl, model_analysis_pb.HttpArchiveContents_Key_TAR_XZ, archive)
		require.NoError(t, result.err)
		require.Equal(t, expectedTarballContents, bct.readHTTPArchiveContents(ctx, t, result.value))
	})

	t.Run("Zip", func(t *testing.T) {
		// ZIP archives don't store permissions in a portable
		// way, meaning that all files are made executable.
		archive := newTestZIPArchive(t, []testArchiveEntry{
			{name: "dir/"},
			{name: "dir/file.txt", body: "Hello"},
			{name: "dir/empty"},
			{name: "implicit/nested/file.txt", body: "Nested"},
		})
		result := bct.computeHTTPArchiveContents(ctx, t, ctrl, model_analysis_pb.HttpArchiveContents_Key_ZIP, archive)
		require.NoError(t, result.err)
		require.Equal(t, map[string]string{
			"dir/":                     "directory",
			"dir/empty":                "executable:",
			"dir/file.txt":             "executable:Hello",
			"implicit/":                "directory",
			"implicit/nested/":         "directory",
			"implicit/nested/file.txt": "executable:Nested",
		}, bct.readHTTPArchiveContents(ctx, t, result.value))
	})

	t.Run("InvalidArchives", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			entries []testArchiveEntry
			err     string
		}{
			{
				name: "PathEscapesArchive",
				entries: []testArchiveEntry{
					{name: "../file.txt", typeflag: tar.TypeReg, body: "Hello"},
				},
				err: "invalid path \"../file.txt\": path resolves to a location outside the build directory",
			},
			{
				name: "DuplicateFile",
				entries: []testArchiveEntry{
					{name: "file.txt", typeflag: tar.TypeReg, body: "Hello"},
					{name: "file.txt", typeflag: tar.TypeReg, body: "Hello"},
				},
				err: "invalid path \"file.txt\": path resolves to an already existing file",
			},
			{
				name: "HardLinkToNonExistentFile",
				entries: []testArchiveEntry{
					{name: "hardlink", typeflag: tar.TypeLink, linkname: "file.txt"},
				},
				err: "invalid hard link target \"file.txt\": path does not resolve to a previously extracted regular file",
			},
			{
				name: "HardLinkToDirectory",
				entries: []testArchiveEntry{
					{name: "dir/", typeflag: tar.TypeDir},
					{name: "hardlink", typeflag: tar.TypeLink, linkname: "dir"},
				},
				err: "invalid hard link target \"dir\": path does not resolve to a previously extracted regular file",
			},
			{
				name: "SymlinkEscapesArchive",
				entries: []testArchiveEntry{
					{name: "link", typeflag: tar.TypeSymlink, linkname: "../outside"},
				},
				err: "archive contains one or more symbolic links that potentially escape the archive's root directory",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				archive := newTestTarball(t, newGzipCompressor, tc.entries)
				result := bct.computeHTTPArchiveContents(ctx, t, ctrl, model_analysis_pb.HttpArchiveContents_Key_TAR_GZ, archive)
				require.EqualError(t, result.err, tc.err)
			})
		}
	})

	t.Run("Progress", func(t *testing.T) {
		// requireProgress checks that progress messages report
		// an increasing number of bytes, starting at zero.
		requireProgress := func(t *testing.T, progress []*model_analysis_pb.HttpArchiveContents_Progress, totalBytes uint64) []uint64 {
			var current []uint64
			for i, p := range progress {
				require.Equal(t, totalBytes, p.Extracted.GetTotal())
				require.Equal(t, model_evaluation_pb.ProgressCounter_BYTES, p.Extracted.GetUnit())
				require.LessOrEqual(t, p.Extracted.GetCurrent(), totalBytes)
				if i > 0 {
					require.Greater(t, p.Extracted.GetCurrent(), current[i-1])
				}
				current = append(current, p.Extracted.GetCurrent())
			}
			return current
		}

		t.Run("Tarball", func(t *testing.T) {
			// Progress of tarballs is based on the number
			// of compressed bytes read. As random data
			// can't be compressed, progress should be
			// reported for every mebibyte.
			archive := newTestTarball(t, newGzipCompressor, []testArchiveEntry{
				{name: "random", typeflag: tar.TypeReg, body: testRandomData(3 << 20)},
			})
			result := bct.computeHTTPArchiveContents(ctx, t, ctrl, model_analysis_pb.HttpArchiveContents_Key_TAR_GZ, archive)
			require.NoError(t, result.err)

			current := requireProgress(t, result.progress, uint64(len(archive)))
			require.Len(t, current, 4)
			require.Equal(t, uint64(0), current[0])
		})

		t.Run("Zip", func(t *testing.T) {
			// Progress of ZIP archives is based on the
			// compressed size of the files extracted.
			archive := newTestZIPArchive(t, []testArchiveEntry{
				{name: "small", body: "Hello"},
				{name: "random", body: testRandomData(2 << 20)},
			})
			result := bct.computeHTTPArchiveContents(ctx, t, ctrl, model_analysis_pb.HttpArchiveContents_Key_ZIP, archive)
			require.NoError(t, result.err)

			zipReader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
			require.NoError(t, err)
			var compressedSizeBytes uint64
			for _, file := range zipReader.File {
				compressedSizeBytes += file.CompressedSize64
			}
			require.Equal(t, []uint64{0, compressedSizeBytes}, requireProgress(t, result.progress, compressedSizeBytes))
		})
	})
}
