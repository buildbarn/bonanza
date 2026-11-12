package build

import (
	"strings"
	"testing"

	"bonanza.build/pkg/glob"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/stretchr/testify/require"
)

func TestMatchIgnoredDirectory(t *testing.T) {
	nfa, err := glob.NewNFAFromPatterns([]string{".git", "**/node_modules", "third_party/*/build"}, nil)
	require.NoError(t, err)
	var root glob.Matcher
	root.Initialize(nfa)

	// isIgnored walks a slash separated path, returning whether
	// the final component is ignored. It fails if any of the
	// parent directories is ignored.
	isIgnored := func(p string) bool {
		m := &root
		components := strings.Split(p, "/")
		for i, component := range components {
			ignored, child := matchIgnoredDirectory(m, path.MustNewComponent(component))
			if i == len(components)-1 {
				return ignored
			}
			require.False(t, ignored, "parent %#v of %#v is ignored", component, p)
			m = child
		}
		panic("unreachable")
	}

	require.True(t, isIgnored(".git"))
	require.False(t, isIgnored("src/.git"))
	require.False(t, isIgnored(".github"))
	require.True(t, isIgnored("node_modules"))
	require.True(t, isIgnored("a/b/node_modules"))
	require.False(t, isIgnored("a/b/node_modules_extra"))
	require.True(t, isIgnored("third_party/foo/build"))
	require.False(t, isIgnored("third_party/foo/bar/build"))
	require.False(t, isIgnored("third_party"))

	t.Run("NilMatcher", func(t *testing.T) {
		ignored, child := matchIgnoredDirectory(nil, path.MustNewComponent(".git"))
		require.False(t, ignored)
		require.Nil(t, child)
	})

	t.Run("PruneUnmatchableSubtrees", func(t *testing.T) {
		nfa, err := glob.NewNFAFromPatterns([]string{".git"}, nil)
		require.NoError(t, err)
		var root glob.Matcher
		root.Initialize(nfa)
		ignored, child := matchIgnoredDirectory(&root, path.MustNewComponent("src"))
		require.False(t, ignored)
		require.Nil(t, child)
	})
}
