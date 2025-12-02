package virtual

import (
	model_filesystem "bonanza.build/pkg/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
)

// FileFactory is used by ObjectBackedDirectoryFactory to create virtual
// file system objects for files contained in directories that have been
// written to the Object Store.
type FileFactory interface {
	LookupFile(fileContents model_filesystem.FileContentsEntry[object.LocalReference], isExecutable bool) (virtual.LinkableLeaf, error)
	GetDecodingParametersSizeBytes(isFileContentsList bool) int
}
