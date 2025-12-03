package arguments

import (
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
)

// Parse command line arguments in a way that is consistent with how
// Bazel parses them. In the process, load bazelrc files and apply any
// relevant options listed in those files as well.
func Parse(args []string, rootDirectory filesystem.Directory, pathFormat path.Format, workspacePath, homeDirectoryPath, workingDirectoryPath path.Parser) (Command, error) {
	startupFlags, argsParsed, err := ParseStartupFlags(args)
	if err != nil {
		return nil, err
	}
	args = args[argsParsed:]

	bazelRCPaths, err := GetBazelRCPaths(startupFlags, pathFormat, workspacePath, homeDirectoryPath, workingDirectoryPath)
	if err != nil {
		return nil, err
	}

	configurationDirectives, err := ParseBazelRCFiles(bazelRCPaths, rootDirectory, pathFormat, workspacePath, workingDirectoryPath)
	if err != nil {
		return nil, err
	}

	return ParseCommandAndArguments(configurationDirectives, args)
}
