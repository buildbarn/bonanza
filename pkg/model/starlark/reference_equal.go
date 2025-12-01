package starlark

import (
	"bytes"
	"errors"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// ReferenceEqualIdentifierGenerator is called when Starlark values are
// created that only offer reference equality, such as depsets. This
// function is supposed to yield a unique identifier for each Starlark
// value.
type ReferenceEqualIdentifierGenerator func() []byte

// ReferenceEqualIdentifierGeneratorKey is the key under which an
// instance of ReferenceEqualIdentifierGenerator is stored in the thread
// local variables of a Starlark thread.
const ReferenceEqualIdentifierGeneratorKey = "reference_equal_identifier_generator"

// referenceEqual can be embedded into Starlark value types that only
// provide reference equality, such as depsets.
//
// As this implementation allows writing Starlark values to storage and
// restoring them, each value with reference equality needs an
// identifier that is persisted together with the value.
type referenceEqual struct {
	identifier []byte
}

func (re *referenceEqual) Hash(thread *starlark.Thread) (uint32, error) {
	return starlark.Bytes(re.identifier).Hash(thread)
}

func (re *referenceEqual) compareSameType(op syntax.Token, other *referenceEqual) (bool, error) {
	switch op {
	case syntax.EQL:
		return bytes.Equal(re.identifier, other.identifier), nil
	case syntax.NEQ:
		return !bytes.Equal(re.identifier, other.identifier), nil
	default:
		return false, errors.New("values with reference equality cannot be compared for inequality")
	}
}
