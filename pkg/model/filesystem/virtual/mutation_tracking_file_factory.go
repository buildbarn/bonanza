package virtual

import (
	"context"
	"sync/atomic"

	model_filesystem "bonanza.build/pkg/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
)

type mutationTrackingFileFactory struct {
	FileFactory
}

// NewMutationTrackingFileFactory creates a decorator for FileFactory
// that wraps all files returned by the underlying implementation with
// instances that track whether any modifications are made to the file's
// contents. If no changes are made to the file's contents,
// VirtualApply(&ApplyGetFileContents{}) will be able to return the
// reference that was used to create the file. This prevents the
// contents of the file from being loaded unnecessarily.
//
// In bonanza_worker this is used to track which files in the input root
// are left unmodified, so that hashing and uploading can be prevented.
func NewMutationTrackingFileFactory(base FileFactory) FileFactory {
	return &mutationTrackingFileFactory{
		FileFactory: base,
	}
}

func (ff *mutationTrackingFileFactory) LookupFile(fileContents model_filesystem.FileContentsEntry[object.LocalReference], isExecutable bool) (virtual.LinkableLeaf, error) {
	f, err := ff.FileFactory.LookupFile(fileContents, isExecutable)
	if err != nil {
		return nil, err
	}
	return &mutationTrackingFile{
		LinkableLeaf: f,
		fileContents: fileContents,
	}, nil
}

// mutationTrackingFile is a decorator for LinkableLeaf that tracks
// whether any changes are made to the file's contents. When no
// modifications are made, VirtualApply(&ApplyGetFileContents{}) returns
// the originally provided reference.
type mutationTrackingFile struct {
	virtual.LinkableLeaf
	fileContents model_filesystem.FileContentsEntry[object.LocalReference]
	isMutated    atomic.Bool
}

func (f *mutationTrackingFile) VirtualApply(data any) bool {
	if p, ok := data.(*ApplyGetFileContents); ok {
		if !f.isMutated.Load() {
			p.FileContents = f.fileContents
			return true
		}
	}
	return f.LinkableLeaf.VirtualApply(data)
}

func (f *mutationTrackingFile) VirtualSetAttributes(ctx context.Context, in *virtual.Attributes, requested virtual.AttributesMask, attributes *virtual.Attributes) virtual.Status {
	if _, ok := in.GetSizeBytes(); ok {
		f.isMutated.Store(true)
	}
	return f.LinkableLeaf.VirtualSetAttributes(ctx, in, requested, attributes)
}

func (f *mutationTrackingFile) VirtualAllocate(off, size uint64) virtual.Status {
	f.isMutated.Store(true)
	return f.LinkableLeaf.VirtualAllocate(off, size)
}

func (f *mutationTrackingFile) VirtualOpenSelf(ctx context.Context, shareAccess virtual.ShareMask, options *virtual.OpenExistingOptions, requested virtual.AttributesMask, attributes *virtual.Attributes) virtual.Status {
	if options.Truncate {
		f.isMutated.Store(true)
	}
	return f.LinkableLeaf.VirtualOpenSelf(ctx, shareAccess, options, requested, attributes)
}

func (f *mutationTrackingFile) VirtualWrite(buf []byte, offset uint64) (int, virtual.Status) {
	f.isMutated.Store(true)
	return f.LinkableLeaf.VirtualWrite(buf, offset)
}
