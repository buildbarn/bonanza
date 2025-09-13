package analysis

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sort"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

type expandCanonicalTargetPatternEnvironment[TReference any] interface {
	GetTargetPatternExpansionValue(*model_analysis_pb.TargetPatternExpansion_Key) model_core.Message[*model_analysis_pb.TargetPatternExpansion_Value, TReference]
}

// expandCanonicalTargetPattern returns canonical labels for each target
// matched by a canonical target pattern.
func (c *baseComputer[TReference, TMetadata]) expandCanonicalTargetPattern(
	ctx context.Context,
	e expandCanonicalTargetPatternEnvironment[TReference],
	targetPattern label.CanonicalTargetPattern,
	includeManualTargets bool,
	errOut *error,
) iter.Seq[label.CanonicalLabel] {
	return func(yield func(canonicalLabel label.CanonicalLabel) bool) {
		if l, ok := targetPattern.AsCanonicalLabel(); ok {
			// Target pattern refers to a single label. No
			// need to perform actual expansion.
			yield(l)
			*errOut = nil
			return
		}

		targetPatternExpansion := e.GetTargetPatternExpansionValue(&model_analysis_pb.TargetPatternExpansion_Key{
			TargetPattern:        targetPattern.String(),
			IncludeManualTargets: includeManualTargets,
		})
		if !targetPatternExpansion.IsSet() {
			*errOut = evaluation.ErrMissingDependency
			return
		}

		for entry := range btree.AllLeaves(
			ctx,
			c.targetPatternExpansionValueTargetLabelReader,
			model_core.Nested(targetPatternExpansion, targetPatternExpansion.Message.TargetLabels),
			func(entry model_core.Message[*model_analysis_pb.TargetPatternExpansion_Value_TargetLabel, TReference]) (*model_core_pb.DecodableReference, error) {
				return entry.Message.GetParent().GetReference(), nil
			},
			errOut,
		) {
			level, ok := entry.Message.Level.(*model_analysis_pb.TargetPatternExpansion_Value_TargetLabel_Leaf)
			if !ok {
				*errOut = errors.New("not a valid leaf entry")
				return
			}
			targetLabel, err := label.NewCanonicalLabel(level.Leaf)
			if err != nil {
				*errOut = fmt.Errorf("invalid target label %#v: %w", level.Leaf, err)
				return
			}
			if !yield(targetLabel) {
				*errOut = nil
				return
			}
		}
	}
}

func (c *baseComputer[TReference, TMetadata]) ComputeTargetPatternExpansionValue(ctx context.Context, key *model_analysis_pb.TargetPatternExpansion_Key, e TargetPatternExpansionEnvironment[TReference, TMetadata]) (PatchedTargetPatternExpansionValue[TMetadata], error) {
	treeBuilder := btree.NewSplitProllyBuilder(
		/* minimumSizeBytes = */ 32*1024,
		/* maximumSizeBytes = */ 128*1024,
		btree.NewObjectCreatingNodeMerger(
			c.getValueObjectEncoder(),
			c.getReferenceFormat(),
			/* parentNodeComputer = */ func(createdObject model_core.Decodable[model_core.CreatedObject[TMetadata]], childNodes []*model_analysis_pb.TargetPatternExpansion_Value_TargetLabel) model_core.PatchedMessage[*model_analysis_pb.TargetPatternExpansion_Value_TargetLabel, TMetadata] {
				return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetPatternExpansion_Value_TargetLabel {
					return &model_analysis_pb.TargetPatternExpansion_Value_TargetLabel{
						Level: &model_analysis_pb.TargetPatternExpansion_Value_TargetLabel_Parent_{
							Parent: &model_analysis_pb.TargetPatternExpansion_Value_TargetLabel_Parent{
								Reference: patcher.CaptureAndAddDecodableReference(createdObject, e),
							},
						},
					}
				})
			},
		),
	)

	canonicalTargetPattern, err := label.NewCanonicalTargetPattern(key.TargetPattern)
	if err != nil {
		return PatchedTargetPatternExpansionValue[TMetadata]{}, fmt.Errorf("invalid target pattern: %w", err)
	}
	if initialTarget, includeFileTargets, ok := canonicalTargetPattern.AsSinglePackageTargetPattern(); ok {
		// Target pattern of shape "@@a+//b:all",
		// "@@a+//b:all-targets" or "@@a+//b:*".
		canonicalPackage := initialTarget.GetCanonicalPackage()
		packageValue := e.GetPackageValue(&model_analysis_pb.Package_Key{
			Label: canonicalPackage.String(),
		})
		if !packageValue.IsSet() {
			return PatchedTargetPatternExpansionValue[TMetadata]{}, evaluation.ErrMissingDependency
		}

		definition, err := c.lookupTargetDefinitionInTargetList(
			ctx,
			model_core.Nested(packageValue, packageValue.Message.Targets),
			initialTarget.GetTargetName(),
		)
		if err != nil {
			return PatchedTargetPatternExpansionValue[TMetadata]{}, err
		}
		if definition.IsSet() {
			// Package contains an actual target that is
			// named "all", "all-targets" or "*". Prefer
			// matching just that target, as opposed to
			// performing actual wildcard expansion.
			if err := treeBuilder.PushChild(model_core.NewSimplePatchedMessage[TMetadata](
				&model_analysis_pb.TargetPatternExpansion_Value_TargetLabel{
					Level: &model_analysis_pb.TargetPatternExpansion_Value_TargetLabel_Leaf{
						Leaf: initialTarget.String(),
					},
				},
			)); err != nil {
				return PatchedTargetPatternExpansionValue[TMetadata]{}, err
			}
		} else if err := c.addPackageToTargetPatternExpansion(ctx, canonicalPackage, packageValue, includeFileTargets, key.IncludeManualTargets, treeBuilder); err != nil {
			return PatchedTargetPatternExpansionValue[TMetadata]{}, err
		}
	} else if basePackage, includeFileTargets, ok := canonicalTargetPattern.AsRecursiveTargetPattern(); ok {
		// Target pattern of shape "@@a+//b/..." or "@@a+//b/...:*".
		packagesAtAndBelow := e.GetPackagesAtAndBelowValue(&model_analysis_pb.PackagesAtAndBelow_Key{
			BasePackage: basePackage.String(),
		})
		if !packagesAtAndBelow.IsSet() {
			return PatchedTargetPatternExpansionValue[TMetadata]{}, evaluation.ErrMissingDependency
		}

		// Add targets belonging to the current package.
		missingDependencies := false
		if packagesAtAndBelow.Message.PackageAtBasePackage {
			if packageValue := e.GetPackageValue(&model_analysis_pb.Package_Key{
				Label: basePackage.String(),
			}); packageValue.IsSet() {
				if err := c.addPackageToTargetPatternExpansion(ctx, basePackage, packageValue, includeFileTargets, key.IncludeManualTargets, treeBuilder); err != nil {
					return PatchedTargetPatternExpansionValue[TMetadata]{}, err
				}
			} else {
				missingDependencies = true
			}
		}

		// Merge results from child packages. We don't recurse
		// into the results, meaning that the resulting B-tree
		// doesn't have a uniform height. This is likely
		// acceptable, as it prevents duplication of data.
		for _, packagePath := range packagesAtAndBelow.Message.PackagesBelowBasePackage {
			childTargetPattern, err := basePackage.ToRecursiveTargetPatternBelow(packagePath, includeFileTargets)
			if err != nil {
				return PatchedTargetPatternExpansionValue[TMetadata]{}, fmt.Errorf("invalid package path %#v: %w", packagePath)
			}
			childTargetPatternExpansion := e.GetTargetPatternExpansionValue(&model_analysis_pb.TargetPatternExpansion_Key{
				TargetPattern:        childTargetPattern.String(),
				IncludeManualTargets: key.IncludeManualTargets,
			})
			if missingDependencies || !childTargetPatternExpansion.IsSet() {
				missingDependencies = true
				continue
			}

			for _, targetLabel := range childTargetPatternExpansion.Message.TargetLabels {
				if err := treeBuilder.PushChild(
					model_core.Patch(e, model_core.Nested(childTargetPatternExpansion, targetLabel)),
				); err != nil {
					return PatchedTargetPatternExpansionValue[TMetadata]{}, err
				}
			}
		}

		if missingDependencies {
			return PatchedTargetPatternExpansionValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
	} else {
		return PatchedTargetPatternExpansionValue[TMetadata]{}, errors.New("target pattern does not require any expansion")
	}

	targetLabelsList, err := treeBuilder.FinalizeList()
	if err != nil {
		return PatchedTargetPatternExpansionValue[TMetadata]{}, err
	}

	return model_core.NewPatchedMessage(
		&model_analysis_pb.TargetPatternExpansion_Value{
			TargetLabels: targetLabelsList.Message,
		},
		targetLabelsList.Patcher,
	), nil
}

func (c *baseComputer[TReference, TMetadata]) addPackageToTargetPatternExpansion(
	ctx context.Context,
	canonicalPackage label.CanonicalPackage,
	packageValue model_core.Message[*model_analysis_pb.Package_Value, TReference],
	includeFileTargets bool,
	includeManualTargets bool,
	treeBuilder btree.Builder[*model_analysis_pb.TargetPatternExpansion_Value_TargetLabel, TMetadata],
) error {
	var errIter error
	for entry := range btree.AllLeaves(
		ctx,
		c.packageValueTargetReader,
		model_core.Nested(packageValue, packageValue.Message.Targets),
		func(entry model_core.Message[*model_analysis_pb.Package_Value_Target, TReference]) (*model_core_pb.DecodableReference, error) {
			if level, ok := entry.Message.Level.(*model_analysis_pb.Package_Value_Target_Parent_); ok {
				return level.Parent.Reference, nil
			}
			return nil, nil
		},
		&errIter,
	) {
		level, ok := entry.Message.Level.(*model_analysis_pb.Package_Value_Target_Leaf)
		if !ok {
			return errors.New("not a valid leaf entry")
		}
		reportTarget := false
		switch definition := level.Leaf.Definition.GetKind().(type) {
		case *model_starlark_pb.Target_Definition_Alias:
			reportTarget = true
		case *model_starlark_pb.Target_Definition_LabelSetting:
			reportTarget = true
		case *model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget:
			if includeFileTargets {
				reportTarget = true
			}
		case *model_starlark_pb.Target_Definition_RuleTarget:
			// Optionally filter out targets that have tag "manual".
			if includeManualTargets {
				reportTarget = true
			} else {
				tags := definition.RuleTarget.Tags
				_, isManual := sort.Find(
					len(tags),
					func(i int) int { return strings.Compare("manual", tags[i]) },
				)
				reportTarget = !isManual
			}
		case *model_starlark_pb.Target_Definition_SourceFileTarget:
			if includeFileTargets {
				reportTarget = true
			}
		}
		if reportTarget {
			targetName, err := label.NewTargetName(level.Leaf.Name)
			if err != nil {
				return fmt.Errorf("invalid target name %#v: %w", level.Leaf.Name, err)
			}
			if err := treeBuilder.PushChild(model_core.NewSimplePatchedMessage[TMetadata](
				&model_analysis_pb.TargetPatternExpansion_Value_TargetLabel{
					Level: &model_analysis_pb.TargetPatternExpansion_Value_TargetLabel_Leaf{
						Leaf: canonicalPackage.AppendTargetName(targetName).String(),
					},
				},
			)); err != nil {
				return err
			}
		}
	}
	return errIter
}
