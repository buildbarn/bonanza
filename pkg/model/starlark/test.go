package starlark

import (
	"fmt"
	"strings"

	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

// ParseTestSize converts the value of the "size" attribute of a test
// to its enumeration value (e.g., "small" to SMALL).
func ParseTestSize(s string) (model_starlark_pb.TestSize, error) {
	if size, ok := model_starlark_pb.TestSize_value[strings.ToUpper(s)]; ok && s == strings.ToLower(s) {
		return model_starlark_pb.TestSize(size), nil
	}
	return 0, fmt.Errorf("invalid test size %#v", s)
}

// ParseTestTimeout converts the value of the "timeout" attribute of a
// test to its enumeration value (e.g., "short" to SHORT).
func ParseTestTimeout(s string) (model_starlark_pb.TestTimeout, error) {
	if timeout, ok := model_starlark_pb.TestTimeout_value[strings.ToUpper(s)]; ok && s == strings.ToLower(s) {
		return model_starlark_pb.TestTimeout(timeout), nil
	}
	return 0, fmt.Errorf("invalid test timeout %#v", s)
}

// FormatTestSize converts a test size to the value of the "size"
// attribute of a test (e.g., SMALL to "small").
func FormatTestSize(size model_starlark_pb.TestSize) string {
	return strings.ToLower(size.String())
}

// FormatTestTimeout converts a test timeout to the value of the
// "timeout" attribute of a test (e.g., SHORT to "short").
func FormatTestTimeout(timeout model_starlark_pb.TestTimeout) string {
	return strings.ToLower(timeout.String())
}

// GetDefaultTestTimeout returns the timeout of a test whose "timeout"
// attribute is not set, which is determined by its size.
func GetDefaultTestTimeout(size model_starlark_pb.TestSize) model_starlark_pb.TestTimeout {
	switch size {
	case model_starlark_pb.TestSize_SMALL:
		return model_starlark_pb.TestTimeout_SHORT
	case model_starlark_pb.TestSize_MEDIUM:
		return model_starlark_pb.TestTimeout_MODERATE
	case model_starlark_pb.TestSize_LARGE:
		return model_starlark_pb.TestTimeout_LONG
	case model_starlark_pb.TestSize_ENORMOUS:
		return model_starlark_pb.TestTimeout_ETERNAL
	default:
		panic("unknown test size")
	}
}

// newRuleTargetTest constructs the message containing the values of
// attributes that are specific to tests, validating them in the
// process. If no timeout is provided, the timeout corresponding to the
// size of the test is used.
func newRuleTargetTest(size, timeout string, flaky, local bool, shardCount, args []*model_starlark_pb.Select_Group) (*model_starlark_pb.RuleTarget_Test, error) {
	parsedSize, err := ParseTestSize(size)
	if err != nil {
		return nil, fmt.Errorf("attribute \"size\": %w", err)
	}
	parsedTimeout := GetDefaultTestTimeout(parsedSize)
	if timeout != "" {
		parsedTimeout, err = ParseTestTimeout(timeout)
		if err != nil {
			return nil, fmt.Errorf("attribute \"timeout\": %w", err)
		}
	}
	return &model_starlark_pb.RuleTarget_Test{
		Size:       parsedSize,
		Timeout:    parsedTimeout,
		Flaky:      flaky,
		Local:      local,
		ShardCount: shardCount,
		Args:       args,
	}, nil
}
