package initialsizeclass

import (
	"time"

	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ActionTimeoutExtractor is a helper type for extracting the execution
// timeout from an Action, taking the configured maximum timeout value
// into account.
type ActionTimeoutExtractor struct {
	maximumExecutionTimeout time.Duration
}

// NewActionTimeoutExtractor creates a new ActionTimeoutExtractor using
// the parameters provided.
func NewActionTimeoutExtractor(maximumExecutionTimeout time.Duration) *ActionTimeoutExtractor {
	return &ActionTimeoutExtractor{
		maximumExecutionTimeout: maximumExecutionTimeout,
	}
}

// ExtractTimeout extracts the execution timeout field from an Action,
// converting it to a time.Duration. It returns errors in case the
// provided execution timeout is invalid or out of bounds.
func (e ActionTimeoutExtractor) ExtractTimeout(action *encryptedaction_pb.Action) (time.Duration, error) {
	executionTimeoutMessage := action.AdditionalData.GetExecutionTimeout()
	if executionTimeoutMessage == nil {
		return 0, status.Error(codes.InvalidArgument, "No execution timeout provided")
	}
	if err := executionTimeoutMessage.CheckValid(); err != nil {
		return 0, util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid execution timeout")
	}
	executionTimeout := executionTimeoutMessage.AsDuration()
	if executionTimeout < 0 || executionTimeout > e.maximumExecutionTimeout {
		return 0, status.Errorf(
			codes.InvalidArgument,
			"Execution timeout of %s is outside permitted range [0s, %s]",
			executionTimeout,
			e.maximumExecutionTimeout,
		)
	}
	return executionTimeout, nil
}
