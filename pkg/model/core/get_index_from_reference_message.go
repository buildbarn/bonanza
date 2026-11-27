package core

import (
	"bonanza.build/pkg/proto/model/core"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetIndexFromReferenceMessage extracts the one-based index contained in
// a Reference message and converts it to a zero-based index that can,
// for example, be provided to OutgoingReferences.GetOutgoingReference().
func GetIndexFromReferenceMessage(referenceMessage *core.Reference, degree int) (int, error) {
	if referenceMessage == nil {
		return -1, status.Error(codes.InvalidArgument, "No reference message provided")
	}
	index := referenceMessage.GetIndex()
	if index <= 0 || int64(index) > int64(degree) {
		return -1, status.Errorf(codes.InvalidArgument, "Reference message contains index %d, which is outside expected range [1, %d]", index, degree)
	}
	return int(index - 1), nil
}
