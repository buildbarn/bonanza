package virtual

import (
	model_filesystem "bonanza.build/pkg/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
)

type FileFactory interface {
	LookupFile(fileContents model_filesystem.FileContentsEntry[object.LocalReference], isExecutable bool) (virtual.LinkableLeaf, error)
	GetDecodingParametersSizeBytes(isFileContentsList bool) int
}
