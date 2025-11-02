package virtual

import (
	"bytes"
	"io"

	model_filesystem "bonanza.build/pkg/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
	"github.com/buildbarn/bb-storage/pkg/util"
)

type resolvableHandleAllocatingFileFactory struct {
	FileFactory
	handleAllocator virtual.ResolvableHandleAllocator
	errorLogger     util.ErrorLogger
}

// NewResolvableHandleAllocatingFileFactory creates a decorator for
// FileFactory that annotates all files with a resolvable handle. This
// can be used by tools that provide read-only and direct access to all
// files in storage, similar to bb_clientd's "cas" directory.
func NewResolvableHandleAllocatingFileFactory(base FileFactory, handleAllocation virtual.ResolvableHandleAllocation, errorLogger util.ErrorLogger) FileFactory {
	ff := &resolvableHandleAllocatingFileFactory{
		FileFactory: base,
		errorLogger: errorLogger,
	}
	ff.handleAllocator = handleAllocation.AsResolvableAllocator(ff.resolveHandle)
	return ff
}

func (ff *resolvableHandleAllocatingFileFactory) resolveHandle(r io.ByteReader) (virtual.DirectoryChild, virtual.Status) {
	entry, err := model_filesystem.NewFileContentsEntryFromBinary(r, ff.FileFactory.GetDecodingParametersSizeBytes)
	if err != nil {
		return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
	}
	isExecutable, err := r.ReadByte()
	if err != nil {
		return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
	}
	f, err := ff.LookupFile(entry, isExecutable != 0x00)
	if err != nil {
		ff.errorLogger.Log(util.StatusWrap(err, "Failed to look up file"))
		return virtual.DirectoryChild{}, virtual.StatusErrIO
	}
	return virtual.DirectoryChild{}.FromLeaf(f), virtual.StatusOK
}

func computeFileID(fileContents model_filesystem.FileContentsEntry[object.LocalReference], isExecutable bool) io.WriterTo {
	handle := fileContents.AppendBinary(nil)
	if isExecutable {
		handle = append(handle, 0x01)
	} else {
		handle = append(handle, 0x00)
	}
	return bytes.NewBuffer(handle)
}

func (ff *resolvableHandleAllocatingFileFactory) LookupFile(fileContents model_filesystem.FileContentsEntry[object.LocalReference], isExecutable bool) (virtual.LinkableLeaf, error) {
	f, err := ff.FileFactory.LookupFile(fileContents, isExecutable)
	if err != nil {
		return nil, err
	}
	return ff.handleAllocator.New(computeFileID(fileContents, isExecutable)).AsLinkableLeaf(f), nil
}
