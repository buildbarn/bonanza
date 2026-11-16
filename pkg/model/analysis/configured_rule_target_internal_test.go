package analysis

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckProvides(t *testing.T) {
	const (
		ccInfo   = "@@rules_cc+//cc:defs.bzl%CcInfo"
		javaInfo = "@@rules_java+//java:defs.bzl%JavaInfo"
		pyInfo   = "@@rules_python+//python:defs.bzl%PyInfo"
	)
	providersSeen := map[string]struct{}{
		ccInfo:   {},
		javaInfo: {},
	}

	t.Run("NoneAdvertised", func(t *testing.T) {
		require.NoError(t, checkProvides(providersSeen, nil))
	})

	t.Run("AllReturned", func(t *testing.T) {
		// Returning providers that are not advertised is
		// permitted.
		require.NoError(t, checkProvides(providersSeen, []string{ccInfo}))
		require.NoError(t, checkProvides(providersSeen, []string{ccInfo, javaInfo}))
	})

	t.Run("Missing", func(t *testing.T) {
		require.EqualError(
			t,
			checkProvides(providersSeen, []string{ccInfo, pyInfo}),
			"implementation function did not return providers @@rules_python+//python:defs.bzl%PyInfo, even though they are listed in provides",
		)
	})
}
