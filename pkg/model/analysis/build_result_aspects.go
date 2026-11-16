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
	"bonanza.build/pkg/starlark/eval"
)

// getTopLevelAspects computes the aspects that need to be applied to
// the targets that are built, as provided through --aspects and
// --aspects_parameters. Each aspect is preceded by the aspects that it
// requires.
func (c *baseComputer[TReference, TMetadata]) getTopLevelAspects(
	ctx context.Context,
	e BuildResultEnvironment[TReference, TMetadata],
	thread *eval.Thread,
	analysisEncoders *AnalysisEncoders[TReference],
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
	rootRepo label.CanonicalRepo,
	key *model_analysis_pb.BuildResult_Key,
) (model_core.PatchedMessage[[]*model_starlark_pb.AppliedAspect, TMetadata], error) {
	if len(key.Aspects) == 0 {
		if len(key.AspectParameters) > 0 {
			return model_core.PatchedMessage[[]*model_starlark_pb.AppliedAspect, TMetadata]{}, errors.New("aspect parameters were provided, while no aspects were provided")
		}
		return model_core.NewSimplePatchedMessage[TMetadata, []*model_starlark_pb.AppliedAspect](nil), nil
	}

	// Resolve the identifiers of the aspects. Labels of .bzl files
	// are resolved from the perspective of the root module.
	missingDependencies := false
	aspectIdentifiers := make([]string, 0, len(key.Aspects))
	for _, aspect := range key.Aspects {
		separator := strings.LastIndexByte(aspect, '%')
		if separator < 0 {
			return model_core.PatchedMessage[[]*model_starlark_pb.AppliedAspect, TMetadata]{}, fmt.Errorf("invalid aspect %#v: expected <bzl-file-label>%%<aspect_name>", aspect)
		}
		apparentLabel, err := label.NewApparentLabel(aspect[:separator])
		if err != nil {
			return model_core.PatchedMessage[[]*model_starlark_pb.AppliedAspect, TMetadata]{}, fmt.Errorf("invalid label of aspect %#v: %w", aspect, err)
		}
		aspectName, err := label.NewStarlarkIdentifier(aspect[separator+1:])
		if err != nil {
			return model_core.PatchedMessage[[]*model_starlark_pb.AppliedAspect, TMetadata]{}, fmt.Errorf("invalid name of aspect %#v: %w", aspect, err)
		}
		canonicalLabel, err := label.Canonicalize(newLabelResolver(e), rootRepo, apparentLabel)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
			return model_core.PatchedMessage[[]*model_starlark_pb.AppliedAspect, TMetadata]{}, fmt.Errorf("failed to resolve label of aspect %#v: %w", aspect, err)
		}
		aspectIdentifiers = append(aspectIdentifiers, canonicalLabel.AppendStarlarkIdentifier(aspectName).String())
	}
	if missingDependencies {
		return model_core.PatchedMessage[[]*model_starlark_pb.AppliedAspect, TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Expand the list of aspects, so that each aspect is preceded
	// by the aspects that it requires.
	labelAttrAspects := e.GetLabelAttrAspectsValue(&model_analysis_pb.LabelAttrAspects_Key{
		AspectIdentifiers: aspectIdentifiers,
	})
	valueEncodingOptionsFactory, err := c.newValueEncodingOptionsFactory(e, starlarkValueEncoders)
	if err != nil {
		return model_core.PatchedMessage[[]*model_starlark_pb.AppliedAspect, TMetadata]{}, err
	}
	if !labelAttrAspects.IsSet() {
		return model_core.PatchedMessage[[]*model_starlark_pb.AppliedAspect, TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Compute the parameters of the aspects. Parameters apply to all
	// aspects that have a public attr with the same name.
	parameterValues := make(map[string]string, len(key.AspectParameters))
	for _, parameter := range key.AspectParameters {
		if _, ok := parameterValues[parameter.Name]; ok {
			return model_core.PatchedMessage[[]*model_starlark_pb.AppliedAspect, TMetadata]{}, fmt.Errorf("aspect parameter %#v is provided multiple times", parameter.Name)
		}
		parameterValues[parameter.Name] = parameter.Value
	}
	usedParameters := make(map[string]struct{}, len(parameterValues))
	valueEncodingOptions := valueEncodingOptionsFactory.newValueEncodingOptions(ctx, nil)
	aspectParameters := make([]*model_core.Decodable[model_core.CreatedObject[TMetadata]], 0, len(labelAttrAspects.Message.Aspects))
	for _, aspect := range labelAttrAspects.Message.Aspects {
		parameters, err := encodeAspectParameters(
			thread,
			c.referenceFormat,
			analysisEncoders.Encoder,
			valueEncodingOptions,
			model_core.Nested(labelAttrAspects, aspect),
			func(publicAttr *model_starlark_pb.NamedAttr) (eval.Value, error) {
				value, ok := parameterValues[publicAttr.Name]
				if !ok {
					return nil, nil
				}
				usedParameters[publicAttr.Name] = struct{}{}
				return parseAspectParameterValue(publicAttr, value)
			},
		)
		if err != nil {
			return model_core.PatchedMessage[[]*model_starlark_pb.AppliedAspect, TMetadata]{}, err
		}
		aspectParameters = append(aspectParameters, parameters)
	}
	for _, parameter := range key.AspectParameters {
		if _, ok := usedParameters[parameter.Name]; !ok {
			return model_core.PatchedMessage[[]*model_starlark_pb.AppliedAspect, TMetadata]{}, fmt.Errorf("aspect parameter %#v does not correspond to a public attr of any of the aspects", parameter.Name)
		}
	}

	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) ([]*model_starlark_pb.AppliedAspect, error) {
		aspects := make([]*model_starlark_pb.AppliedAspect, 0, len(labelAttrAspects.Message.Aspects))
		for i, aspect := range labelAttrAspects.Message.Aspects {
			appliedAspect := &model_starlark_pb.AppliedAspect{
				Identifier: aspect.Identifier,
			}
			if parameters := aspectParameters[i]; parameters != nil {
				reference, err := patcher.CaptureAndAddDecodableReference(ctx, *parameters, e)
				if err != nil {
					return nil, err
				}
				appliedAspect.Parameters = reference
			}
			aspects = append(aspects, appliedAspect)
		}
		return aspects, nil
	})
}
