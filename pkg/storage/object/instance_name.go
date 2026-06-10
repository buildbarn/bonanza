package object

import (
	"net/url"
	"regexp"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// InstanceName denotes the name of a namespace in storage.
//
// In this implementation instance names can have arbitrary string
// values. This differs from REv2, where the instance name is path-like
// and cannot contain certain keywords.
type InstanceName struct {
	value string
}

const validInstanceNameComponentPattern = `[0-9A-Za-z]+`

// Be conservative about what what kinds of instance names we accept.
// It's often desirable to embed instance names in pathnames and URLs.
var (
	validInstanceNameRegexp       = regexp.MustCompile(`^(` + validInstanceNameComponentPattern + `(/` + validInstanceNameComponentPattern + `)*)?$`)
	errInvalidInstanceNamePattern = status.Error(codes.InvalidArgument, "Instance name must consist of zero or more components matching "+validInstanceNameComponentPattern+" separated by a forward slash")
)

// NewInstanceName creates a new InstanceName that corresponds to the
// provided value.
func NewInstanceName(value string) (InstanceName, error) {
	if !validInstanceNameRegexp.MatchString(value) {
		return InstanceName{}, errInvalidInstanceNamePattern
	}
	return InstanceName{
		value: value,
	}, nil
}

// WithLocalReference upgrades a LocalReference to a GlobalReference
// that is associated with the current instance name, allowing it to be
// used as part of an RPC.
func (in InstanceName) WithLocalReference(localReference LocalReference) GlobalReference {
	return GlobalReference{
		InstanceName:   in,
		LocalReference: localReference,
	}
}

// WithReferenceFormat associates the instance name with a reference
// format, thereby turning it into a namespace in which objects are
// stored.
func (in InstanceName) WithReferenceFormat(referenceFormat ReferenceFormat) Namespace {
	return Namespace{
		InstanceName:    in,
		ReferenceFormat: referenceFormat,
	}
}

func (in InstanceName) String() string {
	return in.value
}

// AsURLSafeComponent escapes the instance name string, so that it can
// safely be embedded into URLs. All slashes are are converted to %2F.
// If the instance name is empty, special value "-" is returned.
func (in InstanceName) AsURLSafeComponent() string {
	if in.value == "" {
		return "-"
	}
	return url.PathEscape(in.value)
}
