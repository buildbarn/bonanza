package label

import (
	"errors"
	"regexp"
	"strings"
)

// CanonicalStarlarkIdentifier is the fully canonical name of a Starlark
// identifier, having the form @@repo+//package:my_code.bzl%identifier.
// It can be used to refer to functions, rules, providers, and other
// named constructs.
type CanonicalStarlarkIdentifier struct {
	value string
}

const (
	validCanonicalStarlarkIdentifierPattern = validCanonicalLabelPattern + `%[a-zA-Z_]\w*`
)

var validCanonicalStarlarkIdentifierRegexp = regexp.MustCompile("^" + validCanonicalStarlarkIdentifierPattern + "$")

var errInvalidCanonicalStarlarkIdentifier = errors.New("canonical Starlark identifier must match " + validCanonicalStarlarkIdentifierPattern)

// NewCanonicalStarlarkIdentifier validates that the provided string is
// a valid canonical Starlark identifier. If so, an instance of
// CanonicalStarlarkIdentifier is returned that wraps its value.
func NewCanonicalStarlarkIdentifier(value string) (CanonicalStarlarkIdentifier, error) {
	if !validCanonicalStarlarkIdentifierRegexp.MatchString(value) {
		return CanonicalStarlarkIdentifier{}, errInvalidCanonicalStarlarkIdentifier
	}

	identifierOffset := strings.LastIndexByte(value, '%')
	return CanonicalStarlarkIdentifier{
		value: removeLabelTargetNameIfRedundant(value[:identifierOffset]) + value[identifierOffset:],
	}, nil
}

func (i CanonicalStarlarkIdentifier) String() string {
	return i.value
}

// GetCanonicalLabel returns the label of the file containing the
// Starlark identifier.
func (i CanonicalStarlarkIdentifier) GetCanonicalLabel() CanonicalLabel {
	return CanonicalLabel{
		value: i.value[:strings.LastIndexByte(i.value, '%')],
	}
}

// GetStarlarkIdentifier returns the file local Starlark identifier
// (i.e., the part after the '%').
func (i CanonicalStarlarkIdentifier) GetStarlarkIdentifier() StarlarkIdentifier {
	return StarlarkIdentifier{
		value: i.value[strings.LastIndexByte(i.value, '%')+1:],
	}
}

// ToModuleExtension converts the Starlark identifier of a module
// extension declared in a .bzl file to a string that can be prefixed
// to repos that are declared by the module extension.
//
// The resulting string contains the module instance and the name of
// the module extension object, together with a hash of everything in
// between (e.g., "//foo:extensions.bzl"). This permits a single module
// to declare multiple module extensions having the same name in
// different .bzl files, while still yielding names that only depend on
// the identifier of the module extension.
func (i CanonicalStarlarkIdentifier) ToModuleExtension() ModuleExtension {
	v := i.value[2:]
	identifierIndex := strings.LastIndexByte(v, '%')
	versionIndex := strings.IndexByte(v, '+') + 1
	moduleInstanceEnd := identifierIndex
	if offset := strings.IndexAny(v[versionIndex:identifierIndex], "+/"); offset >= 0 {
		moduleInstanceEnd = versionIndex + offset
	}
	return newModuleExtension(
		v[:moduleInstanceEnd],
		v[moduleInstanceEnd:identifierIndex],
		v[identifierIndex+1:],
	)
}
