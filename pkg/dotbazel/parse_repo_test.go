package dotbazel_test

import (
	"errors"
	"testing"

	"bonanza.build/pkg/dotbazel"
	"bonanza.build/pkg/label"
	"bonanza.build/pkg/starlark/eval"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestParseRepo(t *testing.T) {
	filename := util.Must(label.NewCanonicalLabel("@@foo+//:REPO.bazel"))

	t.Run("Empty", func(t *testing.T) {
		require.NoError(t, dotbazel.ParseRepo(
			eval.NewThread(t.Context(), eval.Universe()),
			"",
			filename,
			func(*eval.Thread, *eval.Builtin, eval.Args) (eval.Value, error) {
				t.Fatal("ignore_directories() should not be called")
				return nil, nil
			},
			func(*eval.Thread, *eval.Builtin, eval.Args) (eval.Value, error) {
				t.Fatal("repo() should not be called")
				return nil, nil
			},
		))
	})

	t.Run("ForwardCalls", func(t *testing.T) {
		// Calls to ignore_directories() and repo() should be
		// forwarded to the provided functions in order, without
		// any validation of the arguments.
		var calls []string
		record := func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			calls = append(calls, b.Name())
			require.Equal(t, "@@foo+//:REPO.bazel", thread.CallStack()[0].Filename)
			return eval.None, nil
		}
		require.NoError(t, dotbazel.ParseRepo(
			eval.NewThread(t.Context(), eval.Universe()),
			`
ignore_directories([".git", "**/node_modules"])
repo(some_future_attribute = 42)
`,
			filename,
			record,
			record,
		))
		require.Equal(t, []string{"ignore_directories", "repo"}, calls)
	})

	t.Run("ErrorPropagation", func(t *testing.T) {
		require.ErrorContains(t, dotbazel.ParseRepo(
			eval.NewThread(t.Context(), eval.Universe()),
			`repo(fail = True)`,
			filename,
			dotbazel.DiscardCall,
			func(*eval.Thread, *eval.Builtin, eval.Args) (eval.Value, error) {
				return nil, errFromRepo
			},
		), errFromRepo.Error())
	})

	t.Run("UnknownFunction", func(t *testing.T) {
		// Only ignore_directories() and repo() are predeclared.
		require.Error(t, dotbazel.ParseRepo(
			eval.NewThread(t.Context(), eval.Universe()),
			`package()`,
			filename,
			dotbazel.DiscardCall,
			dotbazel.DiscardCall,
		))
	})

	t.Run("LoadNotPermitted", func(t *testing.T) {
		require.Error(t, dotbazel.ParseRepo(
			eval.NewThread(t.Context(), eval.Universe()),
			`load("//:foo.bzl", "foo")`,
			filename,
			dotbazel.DiscardCall,
			dotbazel.DiscardCall,
		))
	})
}

var errFromRepo = errors.New("repo() failed")
