package analysis

import (
	"context"
	"sort"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeRegisteredToolchainsForTypeValue(ctx context.Context, key *model_analysis_pb.RegisteredToolchainsForType_Key, e RegisteredToolchainsForTypeEnvironment[TReference, TMetadata]) (PatchedRegisteredToolchainsForTypeValue[TMetadata], error) {
	registeredToolchainsValue := e.GetRegisteredToolchainsValue(&model_analysis_pb.RegisteredToolchains_Key{})
	if !registeredToolchainsValue.IsSet() {
		return PatchedRegisteredToolchainsForTypeValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	registeredToolchains := registeredToolchainsValue.Message.GetToolchainTypes()
	if index, ok := sort.Find(
		len(registeredToolchains),
		func(i int) int {
			return strings.Compare(key.GetToolchainType(), registeredToolchains[i].GetToolchainType())
		},
	); ok {
		// Found one or more toolchains for this type.
		return model_core.NewSimplePatchedMessage[TMetadata](model_analysis_pb.RegisteredToolchainsForType_Value_builder{
			Toolchains: registeredToolchains[index].GetToolchains(),
		}.Build()), nil
	}

	// No toolchains registered for this type.
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.RegisteredToolchainsForType_Value{}), nil
}
