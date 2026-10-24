package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"
)

// pruneQueryTargetDefinition returns a copy of the definition of a
// target that only contains the fields needed by
// model_starlark.GetTargetKind().
func pruneQueryTargetDefinition(definition *model_starlark_pb.Target_Definition) (*model_starlark_pb.Target_Definition, error) {
	switch kind := definition.GetKind().(type) {
	case *model_starlark_pb.Target_Definition_Alias:
		return &model_starlark_pb.Target_Definition{
			Kind: &model_starlark_pb.Target_Definition_Alias{
				Alias: &model_starlark_pb.Alias{},
			},
		}, nil
	case *model_starlark_pb.Target_Definition_LabelSetting:
		return &model_starlark_pb.Target_Definition{
			Kind: &model_starlark_pb.Target_Definition_LabelSetting{
				LabelSetting: &model_starlark_pb.LabelSetting{
					Flag: kind.LabelSetting.Flag,
				},
			},
		}, nil
	case *model_starlark_pb.Target_Definition_PackageGroup:
		return &model_starlark_pb.Target_Definition{
			Kind: &model_starlark_pb.Target_Definition_PackageGroup{
				PackageGroup: &model_starlark_pb.PackageGroup{},
			},
		}, nil
	case *model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget:
		return &model_starlark_pb.Target_Definition{
			Kind: &model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget{
				PredeclaredOutputFileTarget: &model_starlark_pb.PredeclaredOutputFileTarget{},
			},
		}, nil
	case *model_starlark_pb.Target_Definition_RuleTarget:
		return &model_starlark_pb.Target_Definition{
			Kind: &model_starlark_pb.Target_Definition_RuleTarget{
				RuleTarget: &model_starlark_pb.RuleTarget{
					RuleIdentifier: kind.RuleTarget.RuleIdentifier,
				},
			},
		}, nil
	case *model_starlark_pb.Target_Definition_SourceFileTarget:
		return &model_starlark_pb.Target_Definition{
			Kind: &model_starlark_pb.Target_Definition_SourceFileTarget{
				SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
			},
		}, nil
	default:
		return nil, errors.New("unknown target kind")
	}
}

// newQueryTargetTreeBuilder creates a B-tree builder for storing lists
// of QueryTarget messages, as used by the values of QueryResult and
// QueryTargets.
func (c *baseComputer[TReference, TMetadata]) newQueryTargetTreeBuilder(ctx context.Context, capturer model_core.CreatedObjectCapturer[TMetadata]) btree.Builder[*model_analysis_pb.QueryTarget, TMetadata] {
	return btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			/* minimumSizeBytes = */ 32*1024,
			/* maximumSizeBytes = */ 128*1024,
			/* isParent = */ func(target *model_analysis_pb.QueryTarget) bool {
				return target.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			c.getValueObjectEncoder(),
			c.referenceFormat,
			/* parentNodeComputer = */ btree.Capturing(ctx, capturer, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.QueryTarget, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.QueryTarget, TMetadata] {
				return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.QueryTarget {
					return &model_analysis_pb.QueryTarget{
						Level: &model_analysis_pb.QueryTarget_Parent_{
							Parent: &model_analysis_pb.QueryTarget_Parent{
								Reference: patcher.AddDecodableReference(createdObject),
							},
						},
					}
				})
			}),
		),
	)
}

type newQueryTargetLeafEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ExistingObjectCapturer[TReference, TMetadata]
	getQueryRuleTargetEnvironment[TReference]

	GetTargetValue(*model_analysis_pb.Target_Key) model_core.Message[*model_analysis_pb.Target_Value, TReference]
}

// newQueryTargetLeaf creates a QueryTarget message for a single target,
// containing its definition with the requested amount of detail.
func newQueryTargetLeaf[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e newQueryTargetLeafEnvironment[TReference, TMetadata],
	targetLabel string,
	definitionDetail model_analysis_pb.QueryTarget_DefinitionDetail,
) (model_core.PatchedMessage[*model_analysis_pb.QueryTarget, TMetadata], error) {
	if definitionDetail == model_analysis_pb.QueryTarget_NONE {
		// Only the label is needed, meaning there is no need to
		// load the target.
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.QueryTarget{
			Level: &model_analysis_pb.QueryTarget_Leaf_{
				Leaf: &model_analysis_pb.QueryTarget_Leaf{
					Label: targetLabel,
				},
			},
		}), nil
	}

	target := e.GetTargetValue(&model_analysis_pb.Target_Key{Label: targetLabel})
	if !target.IsSet() {
		return model_core.PatchedMessage[*model_analysis_pb.QueryTarget, TMetadata]{}, evaluation.ErrMissingDependency
	}
	if definitionDetail == model_analysis_pb.QueryTarget_KIND {
		definition, err := pruneQueryTargetDefinition(target.Message.Definition)
		if err != nil {
			return model_core.PatchedMessage[*model_analysis_pb.QueryTarget, TMetadata]{}, err
		}
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.QueryTarget{
			Level: &model_analysis_pb.QueryTarget_Leaf_{
				Leaf: &model_analysis_pb.QueryTarget_Leaf{
					Label:      targetLabel,
					Definition: definition,
				},
			},
		}), nil
	}

	// For rule targets, also provide the names of the public
	// attributes of the rule, so that clients can display the
	// values of the attributes.
	var publicAttrNames []string
	if ruleTarget, ok := target.Message.Definition.GetKind().(*model_starlark_pb.Target_Definition_RuleTarget); ok {
		ruleIdentifier, err := label.NewCanonicalStarlarkIdentifier(ruleTarget.RuleTarget.RuleIdentifier)
		if err != nil {
			return model_core.PatchedMessage[*model_analysis_pb.QueryTarget, TMetadata]{}, fmt.Errorf("invalid rule identifier %#v: %w", ruleTarget.RuleTarget.RuleIdentifier, err)
		}
		ruleDefinition, err := getQueryRuleDefinition(e, ruleIdentifier)
		if err != nil {
			return model_core.PatchedMessage[*model_analysis_pb.QueryTarget, TMetadata]{}, err
		}
		for _, namedAttr := range ruleDefinition.Message.Attrs {
			if !strings.HasPrefix(namedAttr.Name, "_") {
				publicAttrNames = append(publicAttrNames, namedAttr.Name)
			}
		}
	}
	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.QueryTarget {
		return &model_analysis_pb.QueryTarget{
			Level: &model_analysis_pb.QueryTarget_Leaf_{
				Leaf: &model_analysis_pb.QueryTarget_Leaf{
					Label:           targetLabel,
					Definition:      model_core.Patch(e, model_core.Nested(target, target.Message.Definition)).Merge(patcher),
					PublicAttrNames: publicAttrNames,
				},
			},
		}
	}), nil
}

type buildQueryTargetsEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.CreatedObjectCapturer[TMetadata]
	newQueryTargetLeafEnvironment[TReference, TMetadata]

	GetQueryTargetsValue(key model_core.PatchedMessage[*model_analysis_pb.QueryTargets_Key, TMetadata]) model_core.Message[*model_analysis_pb.QueryTargets_Value, TReference]
}

// buildQueryTargets creates a B-tree of QueryTarget messages that
// contains the labels and definitions of the targets whose labels are
// contained in a list of TargetLabel messages.
//
// Labels stored in the list itself are processed directly, while
// labels stored in child objects are processed by requesting
// QueryTargets. The top-level nodes of the resulting values are
// inserted into the B-tree as is. Though this means that the resulting
// B-tree is not perfectly balanced, it prevents having to load all
// targets into memory.
func (c *baseComputer[TReference, TMetadata]) buildQueryTargets(
	ctx context.Context,
	e buildQueryTargetsEnvironment[TReference, TMetadata],
	targetLabels model_core.Message[[]*model_analysis_pb.TargetLabel, TReference],
	definitionDetail model_analysis_pb.QueryTarget_DefinitionDetail,
) (model_core.PatchedMessage[[]*model_analysis_pb.QueryTarget, TMetadata], error) {
	switch definitionDetail {
	case model_analysis_pb.QueryTarget_NONE, model_analysis_pb.QueryTarget_KIND, model_analysis_pb.QueryTarget_FULL:
	default:
		return model_core.PatchedMessage[[]*model_analysis_pb.QueryTarget, TMetadata]{}, errors.New("unknown target definition detail")
	}

	treeBuilder := c.newQueryTargetTreeBuilder(ctx, e)
	defer treeBuilder.Discard()
	missingDependencies := false
	for i, targetLabel := range targetLabels.Message {
		switch level := targetLabel.Level.(type) {
		case *model_analysis_pb.TargetLabel_Leaf:
			queryTarget, err := newQueryTargetLeaf(e, level.Leaf, definitionDetail)
			if err != nil {
				if errors.Is(err, evaluation.ErrMissingDependency) {
					missingDependencies = true
					continue
				}
				return model_core.PatchedMessage[[]*model_analysis_pb.QueryTarget, TMetadata]{}, fmt.Errorf("target %#v: %w", level.Leaf, err)
			}
			if missingDependencies {
				// No need to construct a B-tree.
				queryTarget.Discard()
				continue
			}
			if err := treeBuilder.PushChild(queryTarget); err != nil {
				return model_core.PatchedMessage[[]*model_analysis_pb.QueryTarget, TMetadata]{}, err
			}
		case *model_analysis_pb.TargetLabel_Parent_:
			patchedReference := model_core.Patch(e, model_core.Nested(targetLabels, level.Parent.Reference))
			queryTargets := e.GetQueryTargetsValue(
				model_core.NewPatchedMessage(
					&model_analysis_pb.QueryTargets_Key{
						TargetLabelsReference: patchedReference.Message,
						DefinitionDetail:      definitionDetail,
					},
					patchedReference.Patcher,
				),
			)
			if missingDependencies || !queryTargets.IsSet() {
				missingDependencies = true
				continue
			}
			for _, queryTarget := range queryTargets.Message.Targets {
				if err := treeBuilder.PushChild(
					model_core.Patch(e, model_core.Nested(queryTargets, queryTarget)),
				); err != nil {
					return model_core.PatchedMessage[[]*model_analysis_pb.QueryTarget, TMetadata]{}, err
				}
			}
		default:
			return model_core.PatchedMessage[[]*model_analysis_pb.QueryTarget, TMetadata]{}, fmt.Errorf("invalid level type for target label at index %d", i)
		}
	}
	if missingDependencies {
		return model_core.PatchedMessage[[]*model_analysis_pb.QueryTarget, TMetadata]{}, evaluation.ErrMissingDependency
	}
	return treeBuilder.FinalizeList()
}

func (c *baseComputer[TReference, TMetadata]) ComputeQueryTargetsValue(ctx context.Context, key model_core.Message[*model_analysis_pb.QueryTargets_Key, TReference], e QueryTargetsEnvironment[TReference, TMetadata]) (PatchedQueryTargetsValue[TMetadata], error) {
	targetLabels, err := model_parser.Dereference(ctx, c.targetLabelReader, model_core.Nested(key, key.Message.TargetLabelsReference))
	if err != nil {
		return PatchedQueryTargetsValue[TMetadata]{}, err
	}
	targets, err := c.buildQueryTargets(ctx, e, targetLabels, key.Message.DefinitionDetail)
	if err != nil {
		return PatchedQueryTargetsValue[TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_analysis_pb.QueryTargets_Value{
			Targets: targets.Message,
		},
		targets.Patcher,
	), nil
}

// QueryTargetsEnvironmentForTesting is used to generate mocks for
// testing ComputeQueryTargetsValue().
type QueryTargetsEnvironmentForTesting QueryTargetsEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
