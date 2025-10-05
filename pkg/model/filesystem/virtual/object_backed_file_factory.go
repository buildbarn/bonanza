package virtual

import (
	"context"

	model_filesystem "bonanza.build/pkg/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/util"
)

type objectBackedFileFactory struct {
	context     context.Context
	fileReader  *model_filesystem.FileReader[object.LocalReference]
	errorLogger util.ErrorLogger
}

func NewObjectBackedFileFactory(ctx context.Context, fileReader *model_filesystem.FileReader[object.LocalReference], errorLogger util.ErrorLogger) FileFactory {
	return &objectBackedFileFactory{
		context:     ctx,
		fileReader:  fileReader,
		errorLogger: errorLogger,
	}
}

func (ff *objectBackedFileFactory) LookupFile(fileContents model_filesystem.FileContentsEntry[object.LocalReference], isExecutable bool) virtual.LinkableLeaf {
	return &objectBackedFile{
		factory:      ff,
		fileContents: fileContents,
		isExecutable: isExecutable,
	}
}

func (ff *objectBackedFileFactory) GetDecodingParametersSizeBytes(isFilecontentsList bool) int {
	return ff.fileReader.GetDecodingParametersSizeBytes(isFilecontentsList)
}

type objectBackedFile struct {
	factory      *objectBackedFileFactory
	fileContents model_filesystem.FileContentsEntry[object.LocalReference]
	isExecutable bool
}

func (objectBackedFile) Link() virtual.Status {
	// As this file is stateless, we don't need to do any explicit
	// bookkeeping for hardlinks.
	return virtual.StatusOK
}

func (objectBackedFile) Unlink() {
}

func (objectBackedFile) VirtualAllocate(off, size uint64) virtual.Status {
	return virtual.StatusErrWrongType
}

func (objectBackedFile) VirtualClose(shareAccess virtual.ShareMask) {}

func (f *objectBackedFile) VirtualGetAttributes(ctx context.Context, requested virtual.AttributesMask, attributes *virtual.Attributes) {
	attributes.SetChangeID(0)
	attributes.SetFileType(filesystem.FileTypeRegularFile)
	attributes.SetHasNamedAttributes(false)
	attributes.SetIsInNamedAttributeDirectory(false)
	permissions := virtual.PermissionsRead
	if f.isExecutable {
		permissions |= virtual.PermissionsExecute
	}
	attributes.SetPermissions(permissions)
	attributes.SetSizeBytes(f.fileContents.EndBytes)
}

func (f *objectBackedFile) VirtualOpenSelf(ctx context.Context, shareAccess virtual.ShareMask, options *virtual.OpenExistingOptions, requested virtual.AttributesMask, attributes *virtual.Attributes) virtual.Status {
	if shareAccess&^virtual.ShareMaskRead != 0 || options.Truncate {
		return virtual.StatusErrAccess
	}
	f.VirtualGetAttributes(f.factory.context, requested, attributes)
	return virtual.StatusOK
}

func (f *objectBackedFile) VirtualRead(buf []byte, offsetBytes uint64) (int, bool, virtual.Status) {
	buf, eof := virtual.BoundReadToFileSize(buf, offsetBytes, f.fileContents.EndBytes)
	ff := f.factory
	nRead, err := ff.fileReader.FileReadAt(ff.context, f.fileContents, buf, offsetBytes)
	if err != nil {
		ff.errorLogger.Log(err)
		return nRead, false, virtual.StatusErrIO
	}
	return nRead, eof, virtual.StatusOK
}

func (f *objectBackedFile) VirtualReadlink(ctx context.Context) ([]byte, virtual.Status) {
	return nil, virtual.StatusErrInval
}

func (f *objectBackedFile) VirtualSeek(offset uint64, regionType filesystem.RegionType) (*uint64, virtual.Status) {
	switch regionType {
	case filesystem.Data:
		if offset >= f.fileContents.EndBytes {
			return nil, virtual.StatusErrNXIO
		}
		return &offset, virtual.StatusOK
	case filesystem.Hole:
		if offset >= f.fileContents.EndBytes {
			return nil, virtual.StatusErrNXIO
		}
		return &f.fileContents.EndBytes, virtual.StatusOK
	default:
		panic("requests for other seek modes should have been intercepted")
	}
}

func (f *objectBackedFile) VirtualSetAttributes(ctx context.Context, in *virtual.Attributes, requested virtual.AttributesMask, out *virtual.Attributes) virtual.Status {
	if _, ok := in.GetPermissions(); ok {
		return virtual.StatusErrPerm
	}
	if _, ok := in.GetSizeBytes(); ok {
		return virtual.StatusErrAccess
	}
	f.VirtualGetAttributes(f.factory.context, requested, out)
	return virtual.StatusOK
}

func (objectBackedFile) VirtualWrite(buf []byte, offset uint64) (int, virtual.Status) {
	panic("request to write to read-only file should have been intercepted")
}

// ApplyGetFileContents can be used to reobtain the FileContentsEntry
// that was provided to LookupFile() to construct the file. This can be
// used to obtain a reference to the underlying file without recomputing
// the file's Merkle tree.
type ApplyGetFileContents struct {
	FileContents model_filesystem.FileContentsEntry[object.LocalReference]
}

func (f *objectBackedFile) VirtualApply(data any) bool {
	switch p := data.(type) {
	case *ApplyGetFileContents:
		p.FileContents = f.fileContents
	default:
		return false
	}
	return true
}

func (objectBackedFile) VirtualOpenNamedAttributes(ctx context.Context, createDirectory bool, requested virtual.AttributesMask, attributes *virtual.Attributes) (virtual.Directory, virtual.Status) {
	if createDirectory {
		return nil, virtual.StatusErrAccess
	}
	return nil, virtual.StatusErrNoEnt
}
