package build

import (
	"testing"

	"bonanza.build/pkg/bazelclient/arguments"

	"github.com/stretchr/testify/require"
)

func TestValidateBuildCommonFlags(t *testing.T) {
	t.Run("NoBrowserURLAndEmptyInstanceName", func(t *testing.T) {
		require.NoError(t, validateBuildCommonFlags(&arguments.CommonFlags{}))
	})

	t.Run("BrowserURLAndNonEmptyInstanceName", func(t *testing.T) {
		require.NoError(t, validateBuildCommonFlags(&arguments.CommonFlags{
			BrowserUrl:         "http://localhost:9982/",
			RemoteInstanceName: "bonanza-demo",
		}))
	})

	t.Run("BrowserURLAndEmptyInstanceName", func(t *testing.T) {
		require.EqualError(
			t,
			validateBuildCommonFlags(&arguments.CommonFlags{
				BrowserUrl: "http://localhost:9982/",
			}),
			"--browser_url requires --remote_instance_name to be non-empty, because browser links include the instance name in their URL path",
		)
	})
}
