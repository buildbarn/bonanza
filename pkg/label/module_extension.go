package label

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"regexp"
	"strings"
)

// ModuleExtension is a string that uniquely identifies a module
// extension. Its value is used as a prefix for the names of repos
// declared by the module extension.
//
// For module extensions declared in .bzl files, it has the form
// ${moduleInstance}+${hash}+${identifier}, where ${identifier} is the
// name of the module extension object, and ${hash} is a short hash of
// the path of the .bzl file within the module instance. The hash
// ensures that module extensions with the same name, but declared in
// different .bzl files, can be distinguished.
//
// Repos declared using use_repo_rule() are placed in a fictive module
// extension having the form ${moduleInstance}+. As this lacks a hash
// and identifier, it cannot collide with any actual module extension.
type ModuleExtension struct {
	value string
}

const (
	moduleExtensionHashLength       = 6
	validModuleExtensionHashPattern = `[a-z2-7]{6}`
	validModuleExtensionPattern     = validModuleInstancePattern + `\+(` + validModuleExtensionHashPattern + `\+` + validStarlarkIdentifierPattern + `)?`
)

var validModuleExtensionRegexp = regexp.MustCompile("^" + validModuleExtensionPattern + "$")

var errInvalidModuleExtension = errors.New("module extension must match " + validModuleExtensionPattern)

var moduleExtensionHashEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newModuleExtension constructs the name of a module extension, given
// the module instance in which it is declared, the part of the
// canonical Starlark identifier between the module instance and the
// '%' (e.g., "//foo:extensions.bzl"), and the name of the module
// extension object.
func newModuleExtension(moduleInstance, midfix, extensionName string) ModuleExtension {
	// Use lowercase characters only, as these names end up being
	// used as directory names on case insensitive file systems.
	sum := sha256.Sum256([]byte(midfix))
	hash := strings.ToLower(moduleExtensionHashEncoding.EncodeToString(sum[:]))[:moduleExtensionHashLength]
	return ModuleExtension{value: moduleInstance + "+" + hash + "+" + extensionName}
}

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
	versionIndex := strings.IndexByte(me.value, '+') + 1
	return ModuleInstance{value: me.value[:versionIndex+strings.IndexByte(me.value[versionIndex:], '+')]}
}

// IsRepoRules returns true if the module extension is the fictive one
// that holds all repos declared using use_repo_rule() in MODULE.bazel.
func (me ModuleExtension) IsRepoRules() bool {
	return strings.HasSuffix(me.value, "+")
}

// GetCanonicalRepoWithModuleExtension returns the canonical repo name
// of a repo that was created as part of this module extension.
func (me ModuleExtension) GetCanonicalRepoWithModuleExtension(repo ApparentRepo) CanonicalRepo {
	return CanonicalRepo{value: me.value + "+" + repo.value}
}
