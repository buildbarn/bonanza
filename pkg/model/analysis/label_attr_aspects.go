package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

func (baseComputer[TReference, TMetadata]) ComputeLabelAttrAspectsValue(ctx context.Context, key *model_analysis_pb.LabelAttrAspects_Key, e LabelAttrAspectsEnvironment[TReference, TMetadata]) (PatchedLabelAttrAspectsValue[TMetadata], error) {
	// Load the definitions of all aspects, including the ones that
	// are required by them, directly or indirectly.
	aspectDefinitions := map[string]model_core.Message[*model_starlark_pb.Aspect_Definition, TReference]{}
	pendingAspectIdentifiers := append([]string(nil), key.GetAspectIdentifiers()...)
	missingDependencies := false
	for len(pendingAspectIdentifiers) > 0 {
		aspectIdentifierStr := pendingAspectIdentifiers[0]
		pendingAspectIdentifiers = pendingAspectIdentifiers[1:]
		if _, ok := aspectDefinitions[aspectIdentifierStr]; ok {
			continue
		}
		aspectIdentifier, err := label.NewCanonicalStarlarkIdentifier(aspectIdentifierStr)
		if err != nil {
			return PatchedLabelAttrAspectsValue[TMetadata]{}, fmt.Errorf("invalid aspect identifier %#v: %w", aspectIdentifierStr, err)
		}
		aspectDefinition, err := getAspectDefinition(e, aspectIdentifier)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
			return PatchedLabelAttrAspectsValue[TMetadata]{}, err
		}
		aspectDefinitions[aspectIdentifierStr] = aspectDefinition
		pendingAspectIdentifiers = append(pendingAspectIdentifiers, aspectDefinition.Message.GetRequires()...)
	}
	if missingDependencies {
		return PatchedLabelAttrAspectsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Order the aspects, so that each aspect is preceded by the
	// aspects that it requires.
	var orderedAspectIdentifiers []string
	states := map[string]bool{}
	var visit func(aspectIdentifier string) error
	visit = func(aspectIdentifier string) error {
		if done, ok := states[aspectIdentifier]; ok {
			if !done {
				return fmt.Errorf("aspect %#v requires itself, either directly or indirectly", aspectIdentifier)
			}
			return nil
		}
		states[aspectIdentifier] = false
		for _, requiredAspectIdentifier := range aspectDefinitions[aspectIdentifier].Message.GetRequires() {
			if err := visit(requiredAspectIdentifier); err != nil {
				return err
			}
		}
		states[aspectIdentifier] = true
		orderedAspectIdentifiers = append(orderedAspectIdentifiers, aspectIdentifier)
		return nil
	}
	for _, aspectIdentifier := range key.GetAspectIdentifiers() {
		if err := visit(aspectIdentifier); err != nil {
			return PatchedLabelAttrAspectsValue[TMetadata]{}, err
		}
	}

	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.LabelAttrAspects_Value {
		aspects := make([]*model_analysis_pb.LabelAttrAspects_Value_Aspect, 0, len(orderedAspectIdentifiers))
		for _, aspectIdentifier := range orderedAspectIdentifiers {
			aspectDefinition := aspectDefinitions[aspectIdentifier]
			var publicAttrs []*model_starlark_pb.NamedAttr
			for _, namedAttr := range aspectDefinition.Message.GetAttrs() {
				if !strings.HasPrefix(namedAttr.GetName(), "_") {
					publicAttrs = append(publicAttrs, model_core.Patch(e, model_core.Nested(aspectDefinition, namedAttr)).Merge(patcher))
				}
			}
			aspects = append(aspects, &model_analysis_pb.LabelAttrAspects_Value_Aspect{
				Identifier:  aspectIdentifier,
				PublicAttrs: publicAttrs,
			})
		}
		return &model_analysis_pb.LabelAttrAspects_Value{
			Aspects: aspects,
		}
	}), nil
}

// LabelAttrAspectsEnvironmentForTesting is used to generate mocks for
// testing ComputeLabelAttrAspectsValue().
type LabelAttrAspectsEnvironmentForTesting LabelAttrAspectsEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
