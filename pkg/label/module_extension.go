package label

import (
	"errors"
	"regexp"
	"strings"
)

// ModuleExtension is a Starlark identifier that corresponds to the name
// of the module extension object declared in a .bzl file, prefixed with
// the name of the module instance containing the module extension. Its
// value is used as a prefix for the names of repos declared by the
// module extension.
type ModuleExtension struct {
	value string
}

const validModuleExtensionPattern = validModuleInstancePattern + `\+` + validStarlarkIdentifierPattern

var validModuleExtensionRegexp = regexp.MustCompile("^" + validModuleExtensionPattern + "$")

var errInvalidModuleExtension = errors.New("module extension must match " + validModuleExtensionPattern)

// NewModuleExtension validates that the provided string is a valid
// module extension name. If so, a ModuleExtension is returned that
// wraps the provided value.
func NewModuleExtension(value string) (ModuleExtension, error) {
	if !validModuleExtensionRegexp.MatchString(value) {
		return ModuleExtension{}, errInvalidModuleExtension
	}
	return ModuleExtension{value: value}, nil
}

func (me ModuleExtension) String() string {
	return me.value
}

// GetModuleInstance returns the leading module instance name that was
// used to construct the name of this module extension.
func (me ModuleExtension) GetModuleInstance() ModuleInstance {
	return ModuleInstance{value: me.value[:strings.LastIndexByte(me.value, '+')]}
}

// GetExtensionName returns the trailing extension name that was used to
// construct the name of this module extension.
func (me ModuleExtension) GetExtensionName() StarlarkIdentifier {
	return StarlarkIdentifier{value: me.value[strings.LastIndexByte(me.value, '+')+1:]}
}

// GetCanonicalRepoWithModuleExtension returns the canonical repo name
// of a repo that was created as part of this module extension.
func (me ModuleExtension) GetCanonicalRepoWithModuleExtension(repo ApparentRepo) CanonicalRepo {
	return CanonicalRepo{value: me.value + "+" + repo.value}
}
