package starlark_test

import (
	"context"
	"maps"
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"
)

// newTestThread creates a Starlark thread that can be used by tests.
func newTestThread(t *testing.T) *eval.Thread {
	return eval.NewThread(t.Context(), eval.Universe())
}

// execBzlFile executes the source of a .bzl file, using the builtins
// that are available to .bzl files and an optional set of additional
// predeclared values. Identifiers are assigned to globals that track
// their own name, such as rules and providers.
func execBzlFile(thread *eval.Thread, filename label.CanonicalLabel, source string, predeclared map[string]eval.Value) (map[string]eval.Value, error) {
	bzlFileBuiltins, _ := model_starlark.GetBuiltins[object.LocalReference, model_core.CreatedObjectTree]()
	allPredeclared := maps.Clone(bzlFileBuiltins)
	maps.Copy(allPredeclared, predeclared)

	f, err := syntax.Parse(filename.String(), []byte(source), syntax.Options{})
	if err != nil {
		return nil, err
	}
	universe := eval.Universe()
	m, err := resolve.File(
		f,
		&resolve.BzlDialect,
		func(name string) bool {
			_, ok := allPredeclared[name]
			return ok
		},
		func(name string) bool {
			_, ok := universe[name]
			return ok
		},
	)
	if err != nil {
		return nil, err
	}
	module := eval.NewInMemoryModule(filename.String(), allPredeclared, nil)
	if err := eval.ExecFile(thread, compile.File(f, m), module); err != nil {
		return nil, err
	}
	globals := module.Globals()
	model_starlark.NameAndExtractGlobals(globals, filename)
	return globals, nil
}

// namedArgs converts alternating names and values to a list of named
// arguments that can be provided to eval.Call().
func namedArgs(pairs ...any) []eval.NamedArg {
	args := make([]eval.NamedArg, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		args = append(args, eval.NamedArg{
			Name:  pairs[i].(string),
			Value: pairs[i+1].(eval.Value),
		})
	}
	return args
}

// createdObjectTreeCapturer is an ObjectCapturer that keeps created
// objects in memory. It can be used by tests that encode values that
// are stored in multiple objects, such as functions.
type createdObjectTreeCapturer struct{}

func (createdObjectTreeCapturer) CaptureCreatedObject(ctx context.Context, createdObject model_core.CreatedObject[model_core.CreatedObjectTree]) (model_core.CreatedObjectTree, error) {
	return model_core.CreatedObjectTree(createdObject), nil
}

func (createdObjectTreeCapturer) CaptureExistingObject(object.LocalReference) model_core.CreatedObjectTree {
	return model_core.CreatedObjectTree{}
}
