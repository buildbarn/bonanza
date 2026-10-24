package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"bonanza.build/pkg/bazelclient/arguments"
	commands_build "bonanza.build/pkg/bazelclient/commands/build"
	commands_info "bonanza.build/pkg/bazelclient/commands/info"
	commands_license "bonanza.build/pkg/bazelclient/commands/license"
	commands_query "bonanza.build/pkg/bazelclient/commands/query"
	commands_version "bonanza.build/pkg/bazelclient/commands/version"
	"bonanza.build/pkg/bazelclient/formatted"
	"bonanza.build/pkg/bazelclient/logging"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
)

func main() {
	// Logger we need to use before flags have been parsed. As
	// enabling/disabling colors is controlled via a flag, leave
	// colors disabled.
	startupLogger := logging.NewConsoleLogger(os.Stderr, formatted.WritePlainText)

	rootDirectory, err := filesystem.NewLocalDirectory(&path.RootBuilder)
	if err != nil {
		startupLogger.Fatal(formatted.Textf("Failed to open root directory: %s", err))
	}

	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		startupLogger.Fatal(formatted.Textf("Failed to obtain user home directory: %s", err))
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		startupLogger.Fatal(formatted.Textf("Failed to obtain working directory: %s", err))
	}

	var workspacePath path.Parser
	workspacePathStr := workingDirectory
	for {
		moduleBazelPath := filepath.Join(workspacePathStr, "MODULE.bazel")
		if _, err := os.Stat(moduleBazelPath); err == nil {
			workspacePath = path.LocalFormat.NewParser(workspacePathStr)
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			startupLogger.Fatal(formatted.Textf("Failed to obtain workspace path: %s", err))
		}
		parent := filepath.Dir(workspacePathStr)
		if parent == workspacePathStr {
			break
		}
		workspacePathStr = parent
	}

	cmd, err := arguments.Parse(
		os.Args[1:],
		rootDirectory,
		path.LocalFormat,
		workspacePath,
		path.LocalFormat.NewParser(homeDirectory),
		path.LocalFormat.NewParser(workingDirectory),
	)
	if err != nil {
		startupLogger.Fatal(formatted.Text(err.Error()))
	}

	switch typedCmd := cmd.(type) {
	case *arguments.BuildCommand:
		commands_build.DoBuild(typedCmd, workspacePath)
	case *arguments.HelpCommand:
		panic("HELP")
	case *arguments.InfoCommand:
		commands_info.DoInfo(typedCmd, workspacePath)
	case *arguments.LicenseCommand:
		commands_license.DoLicense()
	case *arguments.QueryCommand:
		commands_query.DoQuery(typedCmd, workspacePath)
	case *arguments.VersionCommand:
		commands_version.DoVersion(typedCmd)
	default:
		panic("unknown command type")
	}
}
