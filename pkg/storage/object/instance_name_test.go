package object_test

import (
	"testing"

	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestInstanceName(t *testing.T) {
	t.Run("Invalid", func(t *testing.T) {
		for _, str := range []string{
			// Forbid redundant slashes.
			"/",
			"/hello",
			"hello/",
			"hello//world",

			// Don't allow special characters for the time being.
			"_hello",
			"hello-",

			// Don't permit any whitespace characters, as
			// those tend to cause problems in many
			// contexts.
			"no spaces allowed",
			"no\ttabs\tallowed",

			// Reserve "-" and "_" components as special
			// values for either denoting the absence of an
			// instance name, or to terminate them. This can
			// be used by tools like bonanza_browser to
			// embed instance names into URLs.
			"-",
			"_",
		} {
			_, err := object.NewInstanceName(str)
			testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Instance name must consist of zero or more components matching [0-9A-Za-z]+ separated by a forward slash"), err)
		}
	})

	t.Run("Success", func(t *testing.T) {
		for str, urlComponent := range map[string]string{
			"":                 "-",
			"hello":            "hello",
			"hello123":         "hello123",
			"123hello":         "123hello",
			"MyTeam/Project42": "MyTeam%2FProject42",
		} {
			instanceName, err := object.NewInstanceName(str)
			require.NoError(t, err)
			require.Equal(t, str, instanceName.String())
			require.Equal(t, urlComponent, instanceName.AsURLSafeComponent())
		}
	})
}
