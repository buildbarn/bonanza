package starlark

import (
	"fmt"
	"maps"
	"slices"
	"sort"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
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

// Repr writes the representation of the value.
func (ExecGroup[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<exec_group>")
	return nil
}

// Type returns the type name of a Starlark execution group object in
// string form.
func (ExecGroup[TReference, TMetadata]) Type() string {
	return "exec_group"
}

// Truth returns whether a Starlark execution group object is "truthy"
// or "falsy". Starlark execution group objects are always "truthy".
func (ExecGroup[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
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
func (eg *ExecGroup[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_ExecGroup{
				ExecGroup: eg.Encode(),
			},
		},
	), nil
}

func decodeExecGroup[TReference any, TMetadata model_core.ReferenceMetadata](execGroup *model_starlark_pb.ExecGroup) (*ExecGroup[TReference, TMetadata], error) {
	execCompatibleWith := make([]pg_label.ResolvedLabel, 0, len(execGroup.ExecCompatibleWith))
	for _, labelStr := range execGroup.ExecCompatibleWith {
		label, err := pg_label.NewResolvedLabel(labelStr)
		if err != nil {
			return nil, fmt.Errorf("invalid label %#v: %w", labelStr, err)
		}
		execCompatibleWith = append(execCompatibleWith, label)
	}

	toolchains := make([]*ToolchainType[TReference, TMetadata], 0, len(execGroup.Toolchains))
	for i, toolchain := range execGroup.Toolchains {
		toolchainType, err := decodeToolchainType[TReference, TMetadata](toolchain)
		if err != nil {
			return nil, fmt.Errorf("toolchain %d: %w", i, err)
		}
		toolchains = append(toolchains, toolchainType)
	}

	return NewExecGroup(execCompatibleWith, toolchains), nil
}
