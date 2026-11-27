package analysis

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

func (baseComputer[TReference, TMetadata]) ComputeAspectChainValue(ctx context.Context, key *model_analysis_pb.AspectChain_Key, e AspectChainEnvironment[TReference, TMetadata]) (PatchedAspectChainValue[TMetadata], error) {
	aspectIdentifiers := key.GetAspectIdentifiers()
	if len(aspectIdentifiers) == 0 {
		return PatchedAspectChainValue[TMetadata]{}, errors.New("no aspects provided")
	}
	aspectIndicesByIdentifier := make(map[string]uint32, len(aspectIdentifiers))
	for i, aspectIdentifier := range aspectIdentifiers {
		if _, ok := aspectIndicesByIdentifier[aspectIdentifier]; ok {
			return PatchedAspectChainValue[TMetadata]{}, fmt.Errorf("aspect %#v is applied multiple times", aspectIdentifier)
		}
		aspectIndicesByIdentifier[aspectIdentifier] = uint32(i)
	}

	// Load the definitions of all aspects.
	missingDependencies := false
	aspectDefinitions := make([]*model_starlark_pb.Aspect_Definition, 0, len(aspectIdentifiers))
	for _, aspectIdentifierStr := range aspectIdentifiers {
		aspectIdentifier, err := label.NewCanonicalStarlarkIdentifier(aspectIdentifierStr)
		if err != nil {
			return PatchedAspectChainValue[TMetadata]{}, fmt.Errorf("invalid aspect identifier %#v: %w", aspectIdentifierStr, err)
		}
		aspectDefinition, err := getAspectDefinition(e, aspectIdentifier)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
			return PatchedAspectChainValue[TMetadata]{}, err
		}
		aspectDefinitions = append(aspectDefinitions, aspectDefinition.Message)
	}
	if missingDependencies {
		return PatchedAspectChainValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Validate that required aspects precede the aspects that
	// require them, and obtain their indices.
	requiredAspectIndices := make([][]uint32, 0, len(aspectDefinitions))
	for i, aspectDefinition := range aspectDefinitions {
		requiredAspectIdentifiers := aspectDefinition.GetRequires()
		indices := make([]uint32, 0, len(requiredAspectIdentifiers))
		for _, requiredAspectIdentifier := range requiredAspectIdentifiers {
			requiredAspectIndex, ok := aspectIndicesByIdentifier[requiredAspectIdentifier]
			if !ok || requiredAspectIndex >= uint32(i) {
				return PatchedAspectChainValue[TMetadata]{}, fmt.Errorf("aspect %#v requires aspect %#v, which is not applied before it", aspectIdentifiers[i], requiredAspectIdentifier)
			}
			indices = append(indices, requiredAspectIndex)
		}
		requiredAspectIndices = append(requiredAspectIndices, indices)
	}

	// Determine along which attrs aspects propagate.
	attrPropagatingAspects, err := computePropagatingAspects(
		aspectIdentifiers,
		aspectDefinitions,
		requiredAspectIndices,
		"attr_aspects",
		func(aspectDefinition *model_starlark_pb.Aspect_Definition) *model_starlark_pb.Aspect_PropagationEdges {
			return aspectDefinition.GetAttrAspects()
		},
	)
	if err != nil {
		return PatchedAspectChainValue[TMetadata]{}, err
	}

	// Determine to which toolchains aspects propagate.
	toolchainPropagatingAspects, err := computePropagatingAspects(
		aspectIdentifiers,
		aspectDefinitions,
		requiredAspectIndices,
		"toolchains_aspects",
		func(aspectDefinition *model_starlark_pb.Aspect_Definition) *model_starlark_pb.Aspect_PropagationEdges {
			return aspectDefinition.GetToolchainsAspects()
		},
	)
	if err != nil {
		return PatchedAspectChainValue[TMetadata]{}, err
	}

	// Determine which of the preceding aspects have providers that
	// are visible to the last aspect.
	lastAspectDefinition := aspectDefinitions[len(aspectDefinitions)-1]
	var visibleAspectIndices []uint32
	for i, aspectDefinition := range aspectDefinitions[:len(aspectDefinitions)-1] {
		if model_starlark.ProviderSetsSatisfiedBy(lastAspectDefinition.GetRequiredAspectProviders(), aspectDefinition.GetProvides()) {
			visibleAspectIndices = append(visibleAspectIndices, uint32(i))
		}
	}

	return model_core.NewSimplePatchedMessage[TMetadata](model_analysis_pb.AspectChain_Value_builder{
		VisibleAspectIndices:        visibleAspectIndices,
		AttrPropagatingAspects:      attrPropagatingAspects,
		ToolchainPropagatingAspects: toolchainPropagatingAspects,
	}.Build()), nil
}

// computePropagatingAspects determines along which edges of a given
// kind (e.g., attrs of a rule target) aspects propagate. Aspects that
// propagate along an edge cause the aspects they require to propagate
// along the same edge.
func computePropagatingAspects(
	aspectIdentifiers []string,
	aspectDefinitions []*model_starlark_pb.Aspect_Definition,
	requiredAspectIndices [][]uint32,
	parameterName string,
	getPropagationEdges func(*model_starlark_pb.Aspect_Definition) *model_starlark_pb.Aspect_PropagationEdges,
) (*model_analysis_pb.AspectChain_Value_PropagatingAspects, error) {
	// addWithRequiredAspects adds an aspect and all of the aspects
	// it requires, directly or indirectly, to a set of aspects.
	var addWithRequiredAspects func(aspectIndices map[uint32]struct{}, aspectIndex uint32)
	addWithRequiredAspects = func(aspectIndices map[uint32]struct{}, aspectIndex uint32) {
		if _, ok := aspectIndices[aspectIndex]; ok {
			return
		}
		aspectIndices[aspectIndex] = struct{}{}
		for _, requiredAspectIndex := range requiredAspectIndices[aspectIndex] {
			addWithRequiredAspects(aspectIndices, requiredAspectIndex)
		}
	}

	allAspects := map[uint32]struct{}{}
	edgeAspects := map[string]map[uint32]struct{}{}
	for i, aspectDefinition := range aspectDefinitions {
		if propagationEdges := getPropagationEdges(aspectDefinition); propagationEdges != nil {
			switch propagationEdges.WhichKind() {
			case model_starlark_pb.Aspect_PropagationEdges_Fixed_case:
				for _, name := range propagationEdges.GetFixed().GetNames() {
					aspectIndices, ok := edgeAspects[name]
					if !ok {
						aspectIndices = map[uint32]struct{}{}
						edgeAspects[name] = aspectIndices
					}
					addWithRequiredAspects(aspectIndices, uint32(i))
				}
			case model_starlark_pb.Aspect_PropagationEdges_All_case:
				addWithRequiredAspects(allAspects, uint32(i))
			case model_starlark_pb.Aspect_PropagationEdges_Function_case:
				// TODO: Add support for this feature. This
				// requires invoking the function for every
				// target to which the aspect is applied.
				return nil, fmt.Errorf("aspect %#v: computing %s using a function is not supported yet", aspectIdentifiers[i], parameterName)
			default:
				return nil, fmt.Errorf("aspect %#v: invalid %s", aspectIdentifiers[i], parameterName)
			}
		}
	}

	var edges []*model_analysis_pb.AspectChain_Value_PropagatingAspects_Edge
	for _, name := range slices.Sorted(maps.Keys(edgeAspects)) {
		var aspectIndices []uint32
		for _, aspectIndex := range slices.Sorted(maps.Keys(edgeAspects[name])) {
			if _, ok := allAspects[aspectIndex]; !ok {
				aspectIndices = append(aspectIndices, aspectIndex)
			}
		}
		if len(aspectIndices) > 0 {
			edges = append(edges, model_analysis_pb.AspectChain_Value_PropagatingAspects_Edge_builder{
				Name:          name,
				AspectIndices: aspectIndices,
			}.Build())
		}
	}
	return model_analysis_pb.AspectChain_Value_PropagatingAspects_builder{
		AllAspectIndices: slices.Sorted(maps.Keys(allAspects)),
		Edges:            edges,
	}.Build(), nil
}

// AspectChainEnvironmentForTesting is used to generate mocks for
// testing ComputeAspectChainValue().
type AspectChainEnvironmentForTesting AspectChainEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
