package virtual

import (
	"context"
	"io"

	model_filesystem "bonanza.build/pkg/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
	"github.com/buildbarn/bb-storage/pkg/filesystem"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type objectInitializedFileFactory struct {
	context       context.Context
	fileReader    *model_filesystem.FileReader[object.LocalReference]
	fileAllocator virtual.FileAllocator
}

// NewObjectInitializedFileFactory creates a FileFactory that can be
// used to create mutable files whose initial contents correspond to a
// file in storage.
//
// For regular build we want input files to be read-only. However, for
// repository rules we need input files to be writable as well. For
// example, commands executed by repository rules may append data to
// BUILD files that were produced by previous steps.
func NewObjectInitializedFileFactory(ctx context.Context, fileReader *model_filesystem.FileReader[object.LocalReference], fileAllocator virtual.FileAllocator) FileFactory {
	return &objectInitializedFileFactory{
		context:       ctx,
		fileReader:    fileReader,
		fileAllocator: fileAllocator,
	}
}

func (ff *objectInitializedFileFactory) LookupFile(fileContents model_filesystem.FileContentsEntry[object.LocalReference], isExecutable bool) (virtual.LinkableLeaf, error) {
	return ff.fileAllocator.NewFile(
		&objectHoleSource{
			factory:      ff,
			fileContents: fileContents,
			sizeBytes:    int64(fileContents.EndBytes),
		},
		isExecutable,
		fileContents.EndBytes,
		/* shareAccess = */ 0,
	)
}

func (ff *objectInitializedFileFactory) GetDecodingParametersSizeBytes(isFilecontentsList bool) int {
	return ff.fileReader.GetDecodingParametersSizeBytes(isFilecontentsList)
}

type objectHoleSource struct {
	factory      *objectInitializedFileFactory
	fileContents model_filesystem.FileContentsEntry[object.LocalReference]
	sizeBytes    int64
}

func (objectHoleSource) Close() error {
	return nil
}

func (hs *objectHoleSource) ReadAt(p []byte, off int64) (int, error) {
	nRead := 0
	if off < hs.sizeBytes {
		ff := hs.factory
		var err error
		nRead, err = ff.fileReader.FileReadAt(ff.context, hs.fileContents, p[:min(int64(len(p)), hs.sizeBytes-off)], uint64(off))
		if err != nil && err != io.EOF {
			return nRead, err
		}
	}
	clear(p[nRead:])
	return len(p), nil
}

func (hs *objectHoleSource) GetNextRegionOffset(off int64, regionType filesystem.RegionType) (int64, error) {
	if off < 0 {
		return 0, status.Errorf(codes.InvalidArgument, "Negative seek offset: %d", off)
	}
	if off >= hs.sizeBytes {
		return 0, io.EOF
	}
	switch regionType {
	case filesystem.Data:
		return off, nil
	case filesystem.Hole:
		return hs.sizeBytes, nil
	default:
		panic("Unknown region type")
	}
}

func (hs *objectHoleSource) Truncate(size int64) error {
	if hs.sizeBytes > size {
		hs.sizeBytes = size
	}
	return nil
}
