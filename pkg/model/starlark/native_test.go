package starlark_test

import (
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

func TestNativeModuleAndRepoNames(t *testing.T) {
	bzlFileBuiltins, _ := model_starlark.GetBuiltins[object.LocalReference, model_core.CreatedObjectTree]()
	native := bzlFileBuiltins["native"].(starlark.HasAttrs)
	call := func(thread *starlark.Thread, name string) (starlark.Value, error) {
		return starlark.Call(thread, util.Must(native.Attr(thread, name)), nil, nil)
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
			thread := &starlark.Thread{}
			thread.SetLocal(model_starlark.CanonicalPackageKey, util.Must(label.NewCanonicalPackage(canonicalPackage)))
			for i, name := range []string{"repo_name", "module_name", "module_version"} {
				v, err := call(thread, name)
				require.NoError(t, err)
				require.Equal(t, starlark.String(expected[i]), v, name)
			}
		})
	}

	t.Run("NoPackage", func(t *testing.T) {
		// These functions can only be called while a BUILD
		// file or symbolic macro is being evaluated.
		for _, name := range []string{"repo_name", "module_name", "module_version"} {
			_, err := call(&starlark.Thread{}, name)
			require.ErrorContains(t, err, "cannot be obtained from within this context", name)
		}
	})
}
