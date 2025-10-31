package virtual

import (
	model_filesystem "bonanza.build/pkg/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
)

type statefulHandleAllocatingFileFactory struct {
	FileFactory
	handleAllocator virtual.StatefulHandleAllocator
}

// NewStatefulHandleAllocatingFileFactory creates a decorator for
// FileFactory that annotates all files with a stateful handle. This can
// be used if the underlying files may be mutated. For example, this
// decorator can be used in combination with
// NewObjectInitializedFileFactory() to create copy-on-write files,
// whose initial contents are backed by object storage.
func NewStatefulHandleAllocatingFileFactory(base FileFactory, handleAllocator virtual.StatefulHandleAllocator) FileFactory {
	return &statefulHandleAllocatingFileFactory{
		FileFactory:     base,
		handleAllocator: handleAllocator,
	}
}

func (ff *statefulHandleAllocatingFileFactory) LookupFile(fileContents model_filesystem.FileContentsEntry[object.LocalReference], isExecutable bool) (virtual.LinkableLeaf, error) {
	f, err := ff.FileFactory.LookupFile(fileContents, isExecutable)
	if err != nil {
		return nil, err
	}
	return ff.handleAllocator.New().AsLinkableLeaf(f), nil
}
