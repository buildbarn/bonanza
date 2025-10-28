package virtual

import (
	model_filesystem "bonanza.build/pkg/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
)

type statelessHandleAllocatingFileFactory struct {
	FileFactory
	handleAllocator virtual.StatelessHandleAllocator
}

func NewStatelessHandleAllocatingFileFactory(base FileFactory, handleAllocation virtual.StatelessHandleAllocation) FileFactory {
	return &statelessHandleAllocatingFileFactory{
		FileFactory:     base,
		handleAllocator: handleAllocation.AsStatelessAllocator(),
	}
}

func (ff *statelessHandleAllocatingFileFactory) LookupFile(fileContents model_filesystem.FileContentsEntry[object.LocalReference], isExecutable bool) (virtual.LinkableLeaf, error) {
	f, err := ff.FileFactory.LookupFile(fileContents, isExecutable)
	if err != nil {
		return nil, err
	}
	return ff.handleAllocator.New(computeFileID(fileContents, isExecutable)).AsLinkableLeaf(f), nil
}
