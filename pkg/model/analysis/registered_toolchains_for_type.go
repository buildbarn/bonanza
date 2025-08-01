package analysis

import (
	"context"
	"sort"
	"strings"

	"bonanza.build/pkg/evaluation"
	model_core "bonanza.build/pkg/model/core"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	"bonanza.build/pkg/storage/dag"
)

func (c *baseComputer[TReference, TMetadata]) ComputeRegisteredToolchainsForTypeValue(ctx context.Context, key *model_analysis_pb.RegisteredToolchainsForType_Key, e RegisteredToolchainsForTypeEnvironment[TReference, TMetadata]) (PatchedRegisteredToolchainsForTypeValue, error) {
	registeredToolchainsValue := e.GetRegisteredToolchainsValue(&model_analysis_pb.RegisteredToolchains_Key{})
	if !registeredToolchainsValue.IsSet() {
		return PatchedRegisteredToolchainsForTypeValue{}, evaluation.ErrMissingDependency
	}

	registeredToolchains := registeredToolchainsValue.Message.ToolchainTypes
	if index, ok := sort.Find(
		len(registeredToolchains),
		func(i int) int { return strings.Compare(key.ToolchainType, registeredToolchains[i].ToolchainType) },
	); ok {
		// Found one or more toolchains for this type.
		return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](&model_analysis_pb.RegisteredToolchainsForType_Value{
			Toolchains: registeredToolchains[index].Toolchains,
		}), nil
	}

	// No toolchains registered for this type.
	return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](&model_analysis_pb.RegisteredToolchainsForType_Value{}), nil
}
