package dotbazel

import (
	"bonanza.build/pkg/label"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"
)

// DiscardCall is an implementation of a Starlark function that accepts
// any arguments and returns None. It can be provided to ParseRepo() for
// functions in which the caller has no interest.
func DiscardCall(*eval.Thread, *eval.Builtin, eval.Args) (eval.Value, error) {
	return eval.None, nil
}

// ParseRepo parses a REPO.bazel file that may be stored at the root of
// a repository, and executes it.
//
// Calls to ignore_directories() and repo() are forwarded to the
// provided functions. This function intentionally performs no
// unpacking of arguments, so that callers that are only interested in
// one of these functions can disregard how the other is invoked. This
// makes it possible to extend either function, without requiring
// that all callers of ParseRepo() are updated in lockstep.
//
// The file is executed against the provided thread, allowing the
// caller to set thread-local values that may be needed by the
// provided functions (e.g., label resolvers), and to provide a sink
// for print().
func ParseRepo(
	thread *eval.Thread,
	contents string,
	filename label.CanonicalLabel,
	ignoreDirectoriesFunction eval.BuiltinFunc,
	repoFunction eval.BuiltinFunc,
) error {
	predeclared := map[string]eval.Value{
		"ignore_directories": eval.NewBuiltin("ignore_directories", ignoreDirectoriesFunction),
		"repo":               eval.NewBuiltin("repo", repoFunction),
	}

	// Parse, resolve and compile the file.
	filenameStr := filename.String()
	f, err := syntax.Parse(filenameStr, []byte(contents), syntax.Options{})
	if err != nil {
		return err
	}
	universe := eval.Universe()
	m, err := resolve.File(
		f,
		&resolve.RepoDotBazelDialect,
		func(name string) bool {
			_, ok := predeclared[name]
			return ok
		},
		func(name string) bool {
			_, ok := universe[name]
			return ok
		},
	)
	if err != nil {
		return err
	}

	// Execute the file. The filename of the module needs to be a
	// canonical label, so that labels can be resolved relative to
	// the root package of the repository.
	return eval.ExecFile(thread, compile.File(f, m), eval.NewInMemoryModule(filenameStr, predeclared, nil))
}
