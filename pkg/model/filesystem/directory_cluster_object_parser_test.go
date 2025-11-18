package filesystem_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDirectoryClusterObjectParser(t *testing.T) {
	objectParser := model_filesystem.NewDirectoryClusterObjectParser[object.LocalReference]()

	t.Run("InvalidMessage", func(t *testing.T) {
		_, err := objectParser.ParseObject(
			model_core.NewSimpleMessage[object.LocalReference](
				[]byte("Not a valid Protobuf message"),
			),
			nil,
		)
		testutil.RequirePrefixedStatus(t, status.Error(codes.InvalidArgument, "Failed to parse directory: "), err)
	})

	// TODO: Add more testing coverage.
}
