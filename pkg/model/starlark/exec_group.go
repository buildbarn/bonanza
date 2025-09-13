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

type ExecGroup[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	execCompatibleWith []string
	toolchains         []*ToolchainType[TReference, TMetadata]
}

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

func (ExecGroup[TReference, TMetadata]) Type() string {
	return "exec_group"
}

func (ExecGroup[TReference, TMetadata]) Freeze() {}

func (ExecGroup[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (ExecGroup[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("exec_group cannot be hashed")
}

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

func (eg *ExecGroup[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_ExecGroup{
				ExecGroup: eg.Encode(),
			},
		},
	), false, nil
}
