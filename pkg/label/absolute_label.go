package label

import (
	"errors"
)

var errInvalidAbsoluteLabel = errors.New("absolute label must match " + validApparentOrAbsoluteLabelPattern)

// AbsoluteLabel is a label string that does not depend on the package
// from which it is referenced. It is either prefixed with a canonical
// or apparent repo name (e.g., "@repo//pkg:target"), or starts with
// two slashes (e.g., "//pkg:target"). In the latter case it refers to
// a target within the repo from which it is referenced.
//
// Labels of this kind are used in places where there is no notion of
// a current package, such as the names of user-defined build settings
// on the command line (e.g., "--@repo//pkg:target=value").
type AbsoluteLabel struct {
	value string
}

// NewAbsoluteLabel validates that the provided string is a valid
// absolute label. If so, an instance of AbsoluteLabel is returned that
// wraps the value.
func NewAbsoluteLabel(value string) (AbsoluteLabel, error) {
	if !validApparentOrAbsoluteLabelRegexp.MatchString(value) {
		return AbsoluteLabel{}, errInvalidAbsoluteLabel
	}
	return AbsoluteLabel{value: removeLabelTargetNameIfRedundant(value)}, nil
}

// MustNewAbsoluteLabel is identical to NewAbsoluteLabel, except that
// it panics if the provided string is not a valid absolute label. It
// is intended to be used for labels that are hardcoded.
func MustNewAbsoluteLabel(value string) AbsoluteLabel {
	l, err := NewAbsoluteLabel(value)
	if err != nil {
		panic(err)
	}
	return l
}

func (l AbsoluteLabel) String() string {
	return l.value
}
