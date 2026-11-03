package starlark_test

import (
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestNativeModuleAndRepoNames(t *testing.T) {
	bzlFileBuiltins, _ := model_starlark.GetBuiltins[object.LocalReference, model_core.CreatedObjectTree]()
	native := bzlFileBuiltins["native"].(eval.HasAttrs)
	call := func(thread *eval.Thread, name string) (eval.Value, error) {
		return eval.Call(thread, util.Must(native.Attr(thread, name)), eval.Args{})
	}

	for canonicalPackage, expected := range map[string][3]string{
		// Module instances without a version number.
		"@@rules_go+//go": {"rules_go+", "rules_go", ""},
		// Module instances for which multiple versions are
		// permitted to coexist.
		"@@rules_go+0.50.1//go": {"rules_go+0.50.1", "rules_go", "0.50.1"},
		// Repos declared by module extensions should report
		// the module hosting the extension.
		"@@gazelle+0.40.0+go_deps+org_golang_x_lint//lint": {"gazelle+0.40.0+go_deps+org_golang_x_lint", "gazelle", "0.40.0"},
	} {
		t.Run(canonicalPackage, func(t *testing.T) {
			thread := newTestThread(t)
			thread.SetLocal(model_starlark.CanonicalPackageKey, util.Must(label.NewCanonicalPackage(canonicalPackage)))
			for i, name := range []string{"repo_name", "module_name", "module_version"} {
				v, err := call(thread, name)
				require.NoError(t, err)
				require.Equal(t, eval.String(expected[i]), v, name)
			}
		})
	}

	t.Run("NoPackage", func(t *testing.T) {
		// These functions can only be called while a BUILD
		// file or symbolic macro is being evaluated.
		for _, name := range []string{"repo_name", "module_name", "module_version"} {
			_, err := call(newTestThread(t), name)
			require.ErrorContains(t, err, "cannot be obtained from within this context", name)
		}
	})
}

func TestNativeSubpackages(t *testing.T) {
	bzlFileBuiltins, _ := model_starlark.GetBuiltins[object.LocalReference, model_core.CreatedObjectTree]()
	native := bzlFileBuiltins["native"].(eval.HasAttrs)
	subpackages := util.Must(native.Attr(newTestThread(t), "subpackages"))

	// newThread creates a thread that is capable of expanding
	// subpackages, returning the provided package paths.
	newThread := func(t *testing.T, expectedIncludes, expectedExcludes, packagePaths []string) *eval.Thread {
		thread := newTestThread(t)
		thread.SetLocal(model_starlark.SubpackagesExpanderKey, model_starlark.SubpackagesExpander(func(include, exclude []string) ([]string, error) {
			require.Equal(t, expectedIncludes, include)
			require.Equal(t, expectedExcludes, exclude)
			return packagePaths, nil
		}))
		return thread
	}

	t.Run("Success", func(t *testing.T) {
		thread := newThread(t, []string{"**"}, []string{"b/**"}, []string{"a", "c/d"})
		v, err := eval.Call(
			thread,
			subpackages,
			eval.Args{
				Positional: []eval.Value{eval.NewList([]eval.Value{eval.String("**")})},
				Named:      namedArgs("exclude", eval.NewList([]eval.Value{eval.String("b/**")})),
			},
		)
		require.NoError(t, err)
		require.Equal(t, "[\"a\", \"c/d\"]", util.Must(eval.Repr(thread, v)))
	})

	t.Run("Empty", func(t *testing.T) {
		// Matching no packages is an error, unless
		// allow_empty is set.
		include := []eval.Value{eval.NewList([]eval.Value{eval.String("x")})}
		_, err := eval.Call(newThread(t, []string{"x"}, nil, nil), subpackages, eval.Args{Positional: include})
		require.ErrorContains(t, err, "subpackages does not match any packages")

		thread := newThread(t, []string{"x"}, nil, nil)
		v, err := eval.Call(thread, subpackages, eval.Args{
			Positional: include,
			Named:      namedArgs("allow_empty", eval.True),
		})
		require.NoError(t, err)
		require.Equal(t, "[]", util.Must(eval.Repr(thread, v)))
	})

	t.Run("NoContext", func(t *testing.T) {
		// Subpackages can only be expanded while a BUILD file is
		// being evaluated, not while expanding symbolic macros.
		_, err := eval.Call(newTestThread(t), subpackages, eval.Args{Positional: []eval.Value{eval.NewList(nil)}})
		require.ErrorContains(t, err, "subpackages cannot be expanded within this context")
	})
}
