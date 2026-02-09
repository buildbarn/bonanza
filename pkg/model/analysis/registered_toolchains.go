package analysis

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	pg_starlark "bonanza.build/pkg/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"go.starlark.net/starlark"
)

var declaredToolchainInfoProviderIdentifier = util.Must(label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%DeclaredToolchainInfo"))

const toolchainRuleIdentifier = "@@builtins_core+//:exports.bzl%toolchain"

type registeredToolchainExtractingModuleDotBazelHandler[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	context                    context.Context
	computer                   *baseComputer[TReference, TMetadata]
	environment                RegisteredToolchainsEnvironment[TReference, TMetadata]
	labelResolver              label.Resolver
	moduleInstance             label.ModuleInstance
	ignoreDevDependencies      bool
	registeredToolchainsByType map[string][]*model_analysis_pb.RegisteredToolchain
}

func (registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) BazelDep(name label.Module, version *label.ModuleVersion, repoName label.ApparentRepo, devDependency bool) error {
	return nil
}

func (registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) Module(name label.Module, version *label.ModuleVersion, repoName label.ApparentRepo, bazelCompatibility []string) error {
	return nil
}

func (registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) RegisterExecutionPlatforms(platformTargetPatterns []label.ApparentTargetPattern, devDependency bool) error {
	return nil
}

func (h *registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) RegisterToolchains(toolchainTargetPatterns []label.ApparentTargetPattern, devDependency bool) error {
	if !devDependency || !h.ignoreDevDependencies {
		missingDependencies := false
		listReader := h.computer.valueReaders.List
		for _, apparentToolchainTargetPattern := range toolchainTargetPatterns {
			canonicalToolchainTargetPattern, err := label.Canonicalize(h.labelResolver, h.moduleInstance.GetBareCanonicalRepo(), apparentToolchainTargetPattern)
			if err != nil {
				if errors.Is(err, evaluation.ErrMissingDependency) {
					missingDependencies = true
					continue
				}
				return err
			}
			var iterErr error
			for canonicalToolchainLabel := range h.computer.expandCanonicalTargetPattern(
				h.context,
				h.environment,
				canonicalToolchainTargetPattern,
				/* includeManualTargets = */ true,
				&iterErr,
			) {
				visibleTargetValue := h.environment.GetVisibleTargetValue(
					model_core.NewSimplePatchedMessage[TMetadata](
						&model_analysis_pb.VisibleTarget_Key{
							FromPackage:        canonicalToolchainLabel.GetCanonicalPackage().String(),
							ToLabel:            canonicalToolchainLabel.String(),
							PermitAliasNoMatch: true,
						},
					),
				)
				if !visibleTargetValue.IsSet() {
					missingDependencies = true
					continue
				}

				toolchainLabelStr := visibleTargetValue.Message.Label
				if toolchainLabelStr == "" {
					// Target is an alias() that does not
					// have a default condition. Ignore.
					continue
				}
				toolchainLabel, err := label.NewCanonicalLabel(toolchainLabelStr)
				if err != nil {
					return fmt.Errorf("invalid toolchain label %#v: %w", toolchainLabelStr, err)
				}

				targetValue := h.environment.GetTargetValue(&model_analysis_pb.Target_Key{
					Label: toolchainLabelStr,
				})
				if !targetValue.IsSet() {
					missingDependencies = true
					continue
				}
				ruleTarget, ok := targetValue.Message.Definition.GetKind().(*model_starlark_pb.Target_Definition_RuleTarget)
				if !ok {
					return fmt.Errorf("toolchain %#v is not a rule target", toolchainLabelStr)
				}
				if ruleTarget.RuleTarget.RuleIdentifier != toolchainRuleIdentifier {
					// Non-toolchain target.
					continue
				}

				declaredToolchainInfoProvider, err := getProviderFromConfiguredTarget(
					h.environment,
					toolchainLabelStr,
					model_core.NewSimplePatchedMessage[TMetadata]((*model_core_pb.DecodableReference)(nil)),
					declaredToolchainInfoProviderIdentifier,
				)
				if err != nil {
					if errors.Is(err, evaluation.ErrMissingDependency) {
						missingDependencies = true
						continue
					}
					return fmt.Errorf("toolchain %#v: %w", toolchainLabelStr, err)
				}

				var targetSettings []string
				var toolchain, toolchainType *string
				var errIter error
				for key, value := range model_starlark.AllStructFields(
					h.context,
					listReader,
					declaredToolchainInfoProvider,
					&errIter,
				) {
					switch key {
					case "target_settings":
						l, ok := value.Message.Kind.(*model_starlark_pb.Value_List)
						if !ok {
							return fmt.Errorf("target_settings field of DeclaredToolchainInfo of toolchain %#v is not a list", toolchainLabelStr)
						}
						var errIter error
						for targetSetting := range model_starlark.AllListLeafElementsSkippingDuplicateParents(
							h.context,
							listReader,
							model_core.Nested(value, l.List.Elements),
							map[model_core.Decodable[object.LocalReference]]struct{}{},
							&errIter,
						) {
							targetSettingLabel, ok := targetSetting.Message.Kind.(*model_starlark_pb.Value_Label)
							if !ok {
								return fmt.Errorf("target_settings field of DeclaredToolchainInfo of toolchain %#v contains an element that is not a label", toolchainLabelStr)
							}
							targetSettings = append(targetSettings, targetSettingLabel.Label)
						}
						if errIter != nil {
							return errIter
						}
					case "toolchain":
						l, ok := value.Message.Kind.(*model_starlark_pb.Value_Label)
						if !ok {
							return fmt.Errorf("toolchain field of DeclaredToolchainInfo of toolchain %#v is not a label", toolchainLabelStr)
						}
						toolchain = &l.Label
					case "toolchain_type":
						l, ok := value.Message.Kind.(*model_starlark_pb.Value_Label)
						if !ok {
							return fmt.Errorf("toolchain_type field of DeclaredToolchainInfo of toolchain %#v is not a label", toolchainLabelStr)
						}
						toolchainType = &l.Label
					}
				}
				if errIter != nil {
					return errIter
				}
				if toolchain == nil {
					return fmt.Errorf("DeclaredToolchainInfo of toolchain %#v does not contain field toolchain", toolchainLabelStr)
				}
				if toolchainType == nil {
					return fmt.Errorf("DeclaredToolchainInfo of toolchain %#v does not contain field toolchain_type", toolchainLabelStr)
				}

				toolchainPackage := toolchainLabel.GetCanonicalPackage()
				execCompatibleWith, err := h.computer.constraintValuesToConstraints(
					h.context,
					h.environment,
					toolchainPackage,
					ruleTarget.RuleTarget.ExecCompatibleWith,
				)
				if err != nil {
					if !errors.Is(err, evaluation.ErrMissingDependency) {
						return err
					}
					missingDependencies = true
				}

				// Annoyingly enough, target_compatible_with is
				// configurable. Expand select() expressions.
				// TODO: Is this using the right configuration?
				var targetCompatibleWithLabels []string
				for _, selectGroup := range ruleTarget.RuleTarget.TargetCompatibleWith {
					targetCompatibleWithValue, err := getValueFromSelectGroup(
						h.environment,
						model_core.NewSimpleMessage[TReference]((*model_core_pb.DecodableReference)(nil)),
						toolchainLabel.GetCanonicalPackage(),
						selectGroup,
						/* permitNoMatch = */ false,
					)
					if err != nil {
						return err
					}
					targetCompatibleWithList, ok := targetCompatibleWithValue.Kind.(*model_starlark_pb.Value_List)
					if !ok {
						return fmt.Errorf("target_compatible_with of toolchain %#v is not a list", toolchainLabelStr)
					}

					var errIter error
					for element := range btree.AllLeaves(
						h.context,
						listReader,
						model_core.Nested(targetValue, targetCompatibleWithList.List.Elements),
						func(element model_core.Message[*model_starlark_pb.List_Element, TReference]) (*model_core_pb.DecodableReference, error) {
							return element.Message.GetParent().GetReference(), nil
						},
						&errIter,
					) {
						level, ok := element.Message.Level.(*model_starlark_pb.List_Element_Leaf)
						if !ok {
							return fmt.Errorf("invalid list element level type for target_compatible_with of toolchain %#v", toolchainLabelStr)
						}
						label, ok := level.Leaf.Kind.(*model_starlark_pb.Value_Label)
						if !ok {
							return fmt.Errorf("invalid list element type for target_compatible_with of toolchain %#v", toolchainLabelStr)
						}
						targetCompatibleWithLabels = append(targetCompatibleWithLabels, label.Label)
					}
					if errIter != nil {
						return err
					}
				}

				targetCompatibleWith, err := h.computer.constraintValuesToConstraints(
					h.context,
					h.environment,
					toolchainPackage,
					targetCompatibleWithLabels,
				)
				if err != nil {
					if !errors.Is(err, evaluation.ErrMissingDependency) {
						return err
					}
					missingDependencies = true
				}

				if !missingDependencies {
					slices.Sort(targetSettings)
					h.registeredToolchainsByType[*toolchainType] = append(
						h.registeredToolchainsByType[*toolchainType],
						&model_analysis_pb.RegisteredToolchain{
							ExecCompatibleWith:   execCompatibleWith,
							TargetCompatibleWith: targetCompatibleWith,
							TargetSettings:       slices.Compact(targetSettings),
							Toolchain:            *toolchain,
						},
					)
				}
			}
			if iterErr != nil {
				if !errors.Is(err, evaluation.ErrMissingDependency) {
					return fmt.Errorf("failed to expand target pattern %#v: %w", canonicalToolchainTargetPattern.String(), iterErr)
				}
				missingDependencies = true
			}
		}

		if missingDependencies {
			return evaluation.ErrMissingDependency
		}
	}
	return nil
}

func (registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) UseExtension(extensionBzlFile label.ApparentLabel, extensionName label.StarlarkIdentifier, devDependency, isolate bool) (pg_starlark.ModuleExtensionProxy, error) {
	return pg_starlark.NullModuleExtensionProxy, nil
}

func (registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) UseRepoRule(repoRuleBzlFile label.ApparentLabel, repoRuleName label.StarlarkIdentifier) (pg_starlark.RepoRuleProxy, error) {
	return func(name label.ApparentRepo, devDependency bool, attrs map[string]starlark.Value) error {
		return nil
	}, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeRegisteredToolchainsValue(ctx context.Context, key *model_analysis_pb.RegisteredToolchains_Key, e RegisteredToolchainsEnvironment[TReference, TMetadata]) (PatchedRegisteredToolchainsValue[TMetadata], error) {
	registeredToolchainsByType := map[string][]*model_analysis_pb.RegisteredToolchain{}
	if err := c.visitModuleDotBazelFilesBreadthFirst(ctx, e, func(moduleInstance label.ModuleInstance, ignoreDevDependencies bool) pg_starlark.ChildModuleDotBazelHandler {
		return &registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]{
			context:                    ctx,
			computer:                   c,
			environment:                e,
			labelResolver:              newLabelResolver(e),
			moduleInstance:             moduleInstance,
			ignoreDevDependencies:      ignoreDevDependencies,
			registeredToolchainsByType: registeredToolchainsByType,
		}
	}); err != nil {
		return PatchedRegisteredToolchainsValue[TMetadata]{}, err
	}

	toolchainTypes := make([]*model_analysis_pb.RegisteredToolchains_Value_RegisteredToolchainType, 0, len(registeredToolchainsByType))
	for _, toolchainType := range slices.Sorted(maps.Keys(registeredToolchainsByType)) {
		toolchainTypes = append(toolchainTypes, &model_analysis_pb.RegisteredToolchains_Value_RegisteredToolchainType{
			ToolchainType: toolchainType,
			Toolchains:    registeredToolchainsByType[toolchainType],
		})
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.RegisteredToolchains_Value{
		ToolchainTypes: toolchainTypes,
	}), nil
}
