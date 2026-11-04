package starlark_test

import (
	"testing"

	"bonanza.build/pkg/label"
	"bonanza.build/pkg/starlark/eval"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestBzlModuleNaming(t *testing.T) {
	filename := util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl"))
	run := func(t *testing.T, source string) (*eval.Thread, map[string]eval.Value) {
		thread := newTestThread(t)
		globals, err := execBzlFile(thread, filename, source, nil)
		require.NoError(t, err)
		return thread, globals
	}

	t.Run("ImmediatelyAfterAssignment", func(t *testing.T) {
		// Values should be named as soon as they are assigned to
		// a global, so that the name can already be observed by
		// the remainder of the file.
		_, globals := run(t, `
MyInfo = provider()
r = repr(MyInfo)
`)
		require.Equal(t, eval.String("<provider @@rules+//:defs.bzl%MyInfo>"), globals["r"])
	})

	t.Run("FirstAssignmentWins", func(t *testing.T) {
		// If a value is assigned to multiple globals, the first
		// assignment in execution order determines its name,
		// regardless of alphabetical order.
		thread, globals := run(t, `
YInfo = provider()
XInfo = YInfo
`)
		require.Equal(t, "<provider @@rules+//:defs.bzl%YInfo>", util.Must(eval.Repr(thread, globals["XInfo"])))
	})

	t.Run("HelperFunction", func(t *testing.T) {
		// Values created by helper functions are named after
		// the global to which the helper's result is assigned.
		thread, globals := run(t, `
def _make_info():
    return provider()

FooInfo = _make_info()
`)
		require.Equal(t, "<provider @@rules+//:defs.bzl%FooInfo>", util.Must(eval.Repr(thread, globals["FooInfo"])))
	})

	t.Run("NotAssignedDirectly", func(t *testing.T) {
		// Values that are not assigned to a global directly
		// remain unnamed, as is the case with Bazel.
		thread, globals := run(t, `
PROVIDERS = {"a": provider()}
`)
		require.Equal(t, "{\"a\": <provider>}", util.Must(eval.Repr(thread, globals["PROVIDERS"])))
	})
}
