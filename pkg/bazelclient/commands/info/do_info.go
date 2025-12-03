package info

import (
	"fmt"
	"sort"
	"strings"

	"bonanza.build/pkg/bazelclient/arguments"
	"bonanza.build/pkg/bazelclient/commands"
	"bonanza.build/pkg/bazelclient/formatted"
	"bonanza.build/pkg/bazelclient/logging"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
)

// DoInfo implements the "bazel info" command, which prints the
// currently effective values of certain configuration options. Most of
// these pertain to paths on the local system where data is read or
// written.
func DoInfo(args *arguments.InfoCommand, workspacePath path.Parser) {
	logger := logging.NewLoggerFromFlags(&args.CommonFlags)
	commands.ValidateInsideWorkspace(logger, "info", workspacePath)

	workspacePathBuilder, scopeWalker := path.EmptyBuilder.Join(path.NewAbsoluteScopeWalker(path.VoidComponentWalker))
	if err := path.Resolve(workspacePath, scopeWalker); err != nil {
		logger.Fatal(formatted.Textf("Failed to obtain workspace path: %s", err))
	}
	workspacePathStr, err := path.LocalFormat.GetString(workspacePathBuilder)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to obtain workspace path: %s", err))
	}

	keys := map[string]string{
		"workspace": workspacePathStr,
	}

	var keysToPrint []string
	switch len(args.Arguments) {
	case 0:
		keysToPrint = make([]string, 0, len(keys))
		for key := range keys {
			keysToPrint = append(keysToPrint, key)
		}
		sort.Strings(keysToPrint)
	case 1:
		key := args.Arguments[0]
		value, ok := keys[key]
		if !ok {
			logger.Fatal(formatted.Textf("Unknown key: %#v", key))
		}
		fmt.Println(value)
	default:
		keysToPrint = args.Arguments
	}

	unknownKeysSet := map[string]struct{}{}
	var unknownKeysList []string
	for _, key := range keysToPrint {
		if value, ok := keys[key]; ok {
			fmt.Printf("%s: %s\n", key, value)
		} else if _, ok := unknownKeysSet[key]; !ok {
			unknownKeysSet[key] = struct{}{}
			unknownKeysList = append(unknownKeysList, fmt.Sprintf("%#v", key))
		}
	}

	if len(unknownKeysList) > 0 {
		logger.Fatal(formatted.Textf("Unknown key(s): %s", strings.Join(unknownKeysList, ", ")))
	}
}
