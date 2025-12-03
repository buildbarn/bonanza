package commands

import (
	"bonanza.build/pkg/bazelclient/formatted"
	"bonanza.build/pkg/bazelclient/logging"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
)

// ValidateInsideWorkspace validates that the current process is running
// inside of a Bazel workspace. If not, the process is terminated.
func ValidateInsideWorkspace(logger logging.Logger, commandName string, workspacePath path.Parser) {
	if workspacePath == nil {
		logger.Fatal(formatted.Textf("The %#v command is only supported from within a workspace (below a directory having a MODULE.bazel file)", commandName))
	}
}
