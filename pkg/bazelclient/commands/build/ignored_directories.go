package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"

	"bonanza.build/pkg/dotbazel"
	"bonanza.build/pkg/glob"
	"bonanza.build/pkg/label"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/unpack"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
)

// getIgnoredDirectoriesNFA reads the REPO.bazel file stored at the
// root of a module, and returns a nondeterministic finite automaton
// (NFA) for the patterns provided to ignore_directories(). Directories
// matching these patterns should not be uploaded.
//
// If REPO.bazel does not exist or does not call ignore_directories(),
// an NFA is returned that does not match any directories.
func getIgnoredDirectoriesNFA(ctx context.Context, moduleRootDirectory filesystem.Directory, moduleName label.Module) (*glob.NFA, error) {
	f, err := moduleRootDirectory.OpenRead(path.MustNewComponent(dotbazel.RepoFilename))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &glob.NFAMatchingNothing, nil
		}
		return nil, fmt.Errorf("failed to open REPO.bazel: %w", err)
	}
	contents, err := io.ReadAll(io.NewSectionReader(f, 0, math.MaxInt64))
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read REPO.bazel: %w", err)
	}

	thread := eval.NewThread(ctx, eval.Universe())
	thread.Print = func(_ *eval.Thread, msg string) error {
		// TODO: Provide logging sink.
		fmt.Println(msg)
		return nil
	}

	// Only capture the arguments of ignore_directories(). Calls to
	// repo() are processed by the server.
	nfa := &glob.NFAMatchingNothing
	ignoreDirectoriesCalled := false
	if err := dotbazel.ParseRepo(
		thread,
		string(contents),
		moduleName.ToModuleInstance(nil).GetBareCanonicalRepo().GetRootPackage().AppendTargetName(dotbazel.RepoTargetName),
		func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			if ignoreDirectoriesCalled {
				return nil, fmt.Errorf("%s: function can only be invoked once", b.Name())
			}
			var dirs []string
			if err := eval.UnpackArgs(
				thread, b.Name(), args,
				"dirs", unpack.Bind(&dirs, unpack.List(unpack.String)),
			); err != nil {
				return nil, err
			}
			newNFA, err := glob.NewNFAFromPatterns(dirs, nil)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", b.Name(), err)
			}
			nfa = newNFA
			ignoreDirectoriesCalled = true
			return eval.None, nil
		},
		dotbazel.DiscardCall,
	); err != nil {
		return nil, fmt.Errorf("failed to parse REPO.bazel: %w", err)
	}
	return nfa, nil
}

// matchIgnoredDirectory matches the name of a directory against the
// patterns provided to ignore_directories(). The provided matcher must
// be positioned at the parent directory. If the directory is not
// ignored, a matcher is returned that is positioned at the start of
// the paths of the directory's children.
//
// Nil is returned if none of the patterns can match any of the
// directory's children. This permits pruning matching for the entire
// subtree. A nil parent matcher is therefore also accepted.
func matchIgnoredDirectory(parent *glob.Matcher, name path.Component) (bool, *glob.Matcher) {
	if parent == nil {
		return false, nil
	}
	var child glob.Matcher
	child.CopyFrom(parent)
	for _, r := range name.String() {
		if !child.WriteRune(r) {
			return false, nil
		}
	}
	if child.IsMatch() {
		return true, nil
	}
	if !child.WriteRune('/') {
		return false, nil
	}
	return false, &child
}
