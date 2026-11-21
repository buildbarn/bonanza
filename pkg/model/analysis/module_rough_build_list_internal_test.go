package analysis

import (
	"testing"

	"bonanza.build/pkg/dotbazel"
	"bonanza.build/pkg/label"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestModuleDiscoveryQueue(t *testing.T) {
	newModule := func(name, version string) moduleToCheck {
		return moduleToCheck{
			name:    util.Must(label.NewModule(name)),
			version: util.Must(label.NewModuleVersion(version)),
		}
	}
	popAll := func(q *moduleDiscoveryQueue) []moduleToCheck {
		var modules []moduleToCheck
		for {
			module, ok := q.pop()
			if !ok {
				return modules
			}
			modules = append(modules, module)
		}
	}

	t.Run("Empty", func(t *testing.T) {
		q := newModuleDiscoveryQueue()
		_, ok := q.pop()
		require.False(t, ok)
	})

	t.Run("Deduplication", func(t *testing.T) {
		// Each version of a module should only be returned once.
		q := newModuleDiscoveryQueue()
		q.add(newModule("a", "1.0"))
		q.add(newModule("a", "1.0"))
		q.add(newModule("a", "2.0"))
		q.addNodep(newModule("a", "2.0"))
		require.Equal(t, []moduleToCheck{
			newModule("a", "1.0"),
			newModule("a", "2.0"),
		}, popAll(&q))

		q.add(newModule("a", "1.0"))
		_, ok := q.pop()
		require.False(t, ok)
	})

	t.Run("NodepUnfulfilled", func(t *testing.T) {
		// Nodep dependencies on modules that are not part
		// of the dependency graph should be discarded.
		q := newModuleDiscoveryQueue()
		q.add(newModule("a", "1.0"))
		q.addNodep(newModule("b", "1.0"))
		q.addNodep(newModule("b", "2.0"))
		require.Equal(t, []moduleToCheck{
			newModule("a", "1.0"),
		}, popAll(&q))
	})

	t.Run("NodepAlreadyPresent", func(t *testing.T) {
		// If another version of the module is already part
		// of the dependency graph, nodep dependencies should
		// be followed immediately.
		q := newModuleDiscoveryQueue()
		q.add(newModule("a", "1.0"))
		require.Equal(t, []moduleToCheck{
			newModule("a", "1.0"),
		}, popAll(&q))

		q.addNodep(newModule("a", "2.0"))
		require.Equal(t, []moduleToCheck{
			newModule("a", "2.0"),
		}, popAll(&q))
	})

	t.Run("NodepAddedLater", func(t *testing.T) {
		// Nodep dependencies should be held back until the
		// module becomes part of the dependency graph.
		q := newModuleDiscoveryQueue()
		q.addNodep(newModule("a", "2.0"))
		q.addNodep(newModule("a", "3.0"))
		q.add(newModule("b", "1.0"))
		require.Equal(t, []moduleToCheck{
			newModule("b", "1.0"),
		}, popAll(&q))

		q.add(newModule("a", "1.0"))
		require.Equal(t, []moduleToCheck{
			newModule("a", "1.0"),
			newModule("a", "2.0"),
			newModule("a", "3.0"),
		}, popAll(&q))
	})

	t.Run("NodepNotTransitive", func(t *testing.T) {
		// A nodep dependency on a module that is only
		// referenced through other nodep dependencies should
		// not be followed.
		q := newModuleDiscoveryQueue()
		q.addNodep(newModule("a", "1.0"))
		q.addNodep(newModule("a", "2.0"))
		require.Empty(t, popAll(&q))
	})
}

func TestBazelDepCapturingModuleDotBazelHandler(t *testing.T) {
	parse := func(handler *bazelDepCapturingModuleDotBazelHandler, contents string) error {
		return dotbazel.ParseModule(
			contents,
			util.Must(label.NewCanonicalLabel("@@my_module+//:MODULE.bazel")),
			/* localPathFormat = */ nil,
			dotbazel.NewOverrideIgnoringRootHandler(handler),
		)
	}

	t.Run("MultipleDependencies", func(t *testing.T) {
		// Multiple dependencies on the same module should
		// all be captured. Whether these are valid can only be
		// determined after versions are selected.
		var handler bazelDepCapturingModuleDotBazelHandler
		require.NoError(t, parse(&handler, `
bazel_dep(name = "a", version = "1.0")
bazel_dep(name = "a", version = "1.1", repo_name = "a1")
bazel_dep(name = "a", version = "2.0", repo_name = None)
bazel_dep(name = "b", version = "3.0", repo_name = None)
bazel_dep(name = "b", version = "4.0", repo_name = None)
bazel_dep(name = "c")
`))

		newDep := func(name, version string, nodep bool) bazelDep {
			v := util.Must(label.NewModuleVersion(version))
			return bazelDep{
				module:  util.Must(label.NewModule(name)),
				version: &v,
				nodep:   nodep,
			}
		}
		require.Equal(t, []bazelDep{
			newDep("a", "1.0", false),
			newDep("a", "1.1", false),
			newDep("a", "2.0", true),
			newDep("b", "3.0", true),
			newDep("b", "4.0", true),
			{module: util.Must(label.NewModule("c"))},
		}, handler.dependencies)
	})

	t.Run("NodepWithoutVersion", func(t *testing.T) {
		// Nodep dependencies without a version have no
		// effect. Report these as errors, as they are
		// likely mistakes.
		var handler bazelDepCapturingModuleDotBazelHandler
		require.ErrorContains(t, parse(&handler, `
bazel_dep(name = "a", repo_name = None)
`), "dependency on module \"a\" has repo_name = None, but does not specify a version number, meaning it has no effect")
	})

	t.Run("IgnoredDevDependency", func(t *testing.T) {
		// Dev dependencies that are ignored should not be
		// validated.
		handler := bazelDepCapturingModuleDotBazelHandler{
			ignoreDevDependencies: true,
		}
		require.NoError(t, parse(&handler, `
bazel_dep(name = "a", repo_name = None, dev_dependency = True)
`))
		require.Empty(t, handler.dependencies)
	})
}
