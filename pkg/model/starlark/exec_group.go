package starlark

import (
	"errors"
	"maps"
	"slices"
	"sort"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"go.starlark.net/starlark"
)

// ExecGroup represents a Starlark execution group object. Rules may
// have zero or more execution groups. Each execution group may have
// zero or more actions associated with it. Execution groups are used to
// select execution platforms that satisfy the right constraints and for
// which the necessary toolchains are available.
//
// Starlark execution group objects are opaque. This type only
// implements the necessary logic for converting it to a Protobuf
// message, so that subsequent parts of the analysis can consume it.
type ExecGroup[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	execCompatibleWith []string
	toolchains         []*ToolchainType[TReference, TMetadata]
}

// NewExecGroup creates a new Starlark execution group object, given the
// the attributes that are normally provided to the exec_group()
// function.
func NewExecGroup[TReference any, TMetadata model_core.ReferenceMetadata](execCompatibleWith []pg_label.ResolvedLabel, toolchains []*ToolchainType[TReference, TMetadata]) *ExecGroup[TReference, TMetadata] {
	execCompatibleWithStrings := make([]string, 0, len(execCompatibleWith))
	for _, label := range execCompatibleWith {
		execCompatibleWithStrings = append(execCompatibleWithStrings, label.String())
	}
	sort.Strings(execCompatibleWithStrings)

	// Bazel permits listing the same toolchain multiple types, and
	// with different properties. Deduplicate and merge them.
	toolchainsMap := map[string]*ToolchainType[TReference, TMetadata]{}
	for _, toolchain := range toolchains {
		key := toolchain.toolchainType.String()
		if existingToolchain, ok := toolchainsMap[key]; ok {
			toolchainsMap[key] = existingToolchain.Merge(toolchain)
		} else {
			toolchainsMap[key] = toolchain
		}
	}
	deduplicatedToolchains := make([]*ToolchainType[TReference, TMetadata], 0, len(toolchainsMap))
	for _, key := range slices.Sorted(maps.Keys(toolchainsMap)) {
		deduplicatedToolchains = append(deduplicatedToolchains, toolchainsMap[key])
	}

	return &ExecGroup[TReference, TMetadata]{
		execCompatibleWith: slices.Compact(execCompatibleWithStrings),
		toolchains:         deduplicatedToolchains,
	}
}

func (ExecGroup[TReference, TMetadata]) String() string {
	return "<exec_group>"
}

// Type returns the type name of a Starlark execution group object in
// string form.
func (ExecGroup[TReference, TMetadata]) Type() string {
	return "exec_group"
}

// Freeze a Starlark execution group object, so that it can no longer be
// mutated. This has no effect, as Starlark execution group objects are
// already immutable.
func (ExecGroup[TReference, TMetadata]) Freeze() {}

// Truth returns whether a Starlark execution group object is "truthy"
// or "falsy". Starlark execution group objects are always "truthy".
func (ExecGroup[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

// Hash a Starlark execution group, so that it can be placed in a set or
// used as a key in a dict. Starlark execution groups cannot be hashed,
// though.
func (ExecGroup[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("exec_group cannot be hashed")
}

// Encode the definition of a Starlark execution group in the form of a
// Protobuf message, so that it can be written to storage.
func (eg *ExecGroup[TReference, TMetadata]) Encode() *model_starlark_pb.ExecGroup {
	execGroup := model_starlark_pb.ExecGroup{
		ExecCompatibleWith: eg.execCompatibleWith,
		Toolchains:         make([]*model_starlark_pb.ToolchainType, 0, len(eg.toolchains)),
	}
	for _, toolchain := range eg.toolchains {
		execGroup.Toolchains = append(execGroup.Toolchains, toolchain.Encode())
	}
	return &execGroup
}

// EncodeValue returns the definition of a Starlark execution group in
// the form of a Protobuf message of a generic Starlark value.
func (eg *ExecGroup[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_ExecGroup{
				ExecGroup: eg.Encode(),
			},
		},
	), false, nil
}
