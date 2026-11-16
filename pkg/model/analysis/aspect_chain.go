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
	aspectIdentifiers := key.AspectIdentifiers
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
		indices := make([]uint32, 0, len(aspectDefinition.Requires))
		for _, requiredAspectIdentifier := range aspectDefinition.Requires {
			requiredAspectIndex, ok := aspectIndicesByIdentifier[requiredAspectIdentifier]
			if !ok || requiredAspectIndex >= uint32(i) {
				return PatchedAspectChainValue[TMetadata]{}, fmt.Errorf("aspect %#v requires aspect %#v, which is not applied before it", aspectIdentifiers[i], requiredAspectIdentifier)
			}
			indices = append(indices, requiredAspectIndex)
		}
		requiredAspectIndices = append(requiredAspectIndices, indices)
	}

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

	// Determine along which attrs aspects propagate.
	alwaysPropagatingAspects := map[uint32]struct{}{}
	attrPropagatingAspects := map[string]map[uint32]struct{}{}
	for i, aspectDefinition := range aspectDefinitions {
		for _, attrName := range aspectDefinition.AttrAspects {
			if attrName == "*" {
				addWithRequiredAspects(alwaysPropagatingAspects, uint32(i))
			} else {
				aspectIndices, ok := attrPropagatingAspects[attrName]
				if !ok {
					aspectIndices = map[uint32]struct{}{}
					attrPropagatingAspects[attrName] = aspectIndices
				}
				addWithRequiredAspects(aspectIndices, uint32(i))
			}
		}
	}
	var attrPropagatingAspectsList []*model_analysis_pb.AspectChain_Value_AttrPropagatingAspects
	for _, attrName := range slices.Sorted(maps.Keys(attrPropagatingAspects)) {
		var aspectIndices []uint32
		for _, aspectIndex := range slices.Sorted(maps.Keys(attrPropagatingAspects[attrName])) {
			if _, ok := alwaysPropagatingAspects[aspectIndex]; !ok {
				aspectIndices = append(aspectIndices, aspectIndex)
			}
		}
		if len(aspectIndices) > 0 {
			attrPropagatingAspectsList = append(attrPropagatingAspectsList, &model_analysis_pb.AspectChain_Value_AttrPropagatingAspects{
				AttrName:      attrName,
				AspectIndices: aspectIndices,
			})
		}
	}

	// Determine which of the preceding aspects have providers that
	// are visible to the last aspect.
	lastAspectDefinition := aspectDefinitions[len(aspectDefinitions)-1]
	var visibleAspectIndices []uint32
	for i, aspectDefinition := range aspectDefinitions[:len(aspectDefinitions)-1] {
		if model_starlark.ProviderSetsSatisfiedBy(lastAspectDefinition.RequiredAspectProviders, aspectDefinition.Provides) {
			visibleAspectIndices = append(visibleAspectIndices, uint32(i))
		}
	}

	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.AspectChain_Value{
		VisibleAspectIndices:           visibleAspectIndices,
		AlwaysPropagatingAspectIndices: slices.Sorted(maps.Keys(alwaysPropagatingAspects)),
		AttrPropagatingAspects:         attrPropagatingAspectsList,
	}), nil
}

// AspectChainEnvironmentForTesting is used to generate mocks for
// testing ComputeAspectChainValue().
type AspectChainEnvironmentForTesting AspectChainEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
