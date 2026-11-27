package analysis

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"bonanza.build/pkg/dotbazel"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
)

var declaredToolchainInfoProviderIdentifier = util.Must(label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%DeclaredToolchainInfo"))

var toolchainRuleIdentifier = util.Must(label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%toolchain"))

type registeredToolchainExtractingModuleDotBazelHandler[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	context                    context.Context
	computer                   *baseComputer[TReference, TMetadata]
	environment                RegisteredToolchainsEnvironment[TReference, TMetadata]
	valueReaders               *model_starlark.ValueReaders[TReference]
	analysisReaders            *Readers[TReference]
	labelResolver              label.Resolver
	moduleInstance             label.ModuleInstance
	ignoreDevDependencies      bool
	registeredToolchainsByType map[string][]*model_analysis_pb.RegisteredToolchain
	missingDependencies        *bool
}

func (registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) BazelDep(name label.Module, version *label.ModuleVersion, repoName *label.ApparentRepo, devDependency bool) error {
	return nil
}

func (registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) FlagAlias(name string, starlarkFlag label.ApparentLabel) error {
	return nil
}

func (registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) Module(name label.Module, repoName label.ApparentRepo, bazelCompatibility []string) error {
	return nil
}

func (registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) RegisterExecutionPlatforms(platformTargetPatterns []label.ApparentTargetPattern, devDependency bool) error {
	return nil
}

func (h *registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) RegisterToolchains(toolchainTargetPatterns []label.ApparentTargetPattern, devDependency bool) error {
	if !devDependency || !h.ignoreDevDependencies {
		listReader := h.valueReaders.List
		for _, apparentToolchainTargetPattern := range toolchainTargetPatterns {
			canonicalToolchainTargetPattern, err := label.Canonicalize(h.labelResolver, h.moduleInstance.GetBareCanonicalRepo(), apparentToolchainTargetPattern)
			if err != nil {
				if errors.Is(err, evaluation.ErrMissingDependency) {
					*h.missingDependencies = true
					continue
				}
				return err
			}
			// Similar to Bazel, only retain targets matched by
			// wildcards if they are instances of toolchain().
			// Targets referenced explicitly are always retained,
			// and must resolve to a toolchain.
			var iterErr error
			for canonicalToolchainLabel := range h.computer.allQueryLabels(
				h.context,
				h.environment,
				h.analysisReaders,
				canonicalToolchainTargetPattern.ToQueryOfRule(toolchainRuleIdentifier),
				&iterErr,
			) {
				visibleTargetValue := h.environment.GetVisibleTargetValue(
					model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.VisibleTarget_Key{
						FromPackage:        canonicalToolchainLabel.GetCanonicalPackage().String(),
						ToLabel:            canonicalToolchainLabel.String(),
						PermitAliasNoMatch: true,
					}),
				)
				if !visibleTargetValue.IsSet() {
					*h.missingDependencies = true
					continue
				}

				toolchainLabelStr := visibleTargetValue.Message.GetLabel()
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
					*h.missingDependencies = true
					continue
				}
				definition := targetValue.Message.GetDefinition()
				if definition == nil {
					return fmt.Errorf("toolchain %#v does not exist", toolchainLabelStr)
				}
				ruleTarget := definition.GetRuleTarget()
				if ruleTarget == nil {
					return fmt.Errorf("toolchain %#v is not a rule target", toolchainLabelStr)
				}
				if ruleTarget.GetRuleIdentifier() != toolchainRuleIdentifier.String() {
					return fmt.Errorf("toolchain %#v is not a toolchain() target", toolchainLabelStr)
				}

				declaredToolchainInfoProvider, err := getProviderFromConfiguredTarget(
					h.environment,
					toolchainLabelStr,
					model_core.NewSimplePatchedMessage[TMetadata]((*model_core_pb.DecodableReference)(nil)),
					declaredToolchainInfoProviderIdentifier,
				)
				if err != nil {
					if errors.Is(err, evaluation.ErrMissingDependency) {
						*h.missingDependencies = true
						continue
					}
					return fmt.Errorf("toolchain %#v: %w", toolchainLabelStr, err)
				}

				var targetSettings []string
				var toolchain, toolchainType *string
				useTargetPlatformConstraints := false
				var errIter error
				for key, value := range model_starlark.AllStructFields(
					h.context,
					listReader,
					declaredToolchainInfoProvider,
					&errIter,
				) {
					switch key {
					case "target_settings":
						l := value.Message.GetList()
						if l == nil {
							return fmt.Errorf("target_settings field of DeclaredToolchainInfo of toolchain %#v is not a list", toolchainLabelStr)
						}
						var errIter error
						for targetSetting := range model_starlark.AllListLeafElementsSkippingDuplicateParents(
							h.context,
							listReader,
							model_core.Nested(value, l.GetElements()),
							map[model_core.Decodable[object.LocalReference]]struct{}{},
							&errIter,
						) {
							targetSettingLabel := targetSetting.Message.GetLabel()
							if targetSettingLabel == "" {
								return fmt.Errorf("target_settings field of DeclaredToolchainInfo of toolchain %#v contains an element that is not a label", toolchainLabelStr)
							}
							targetSettings = append(targetSettings, targetSettingLabel)
						}
						if errIter != nil {
							return errIter
						}
					case "toolchain":
						l := value.Message.GetLabel()
						if l == "" {
							return fmt.Errorf("toolchain field of DeclaredToolchainInfo of toolchain %#v is not a label", toolchainLabelStr)
						}
						toolchain = &l
					case "toolchain_type":
						l := value.Message.GetLabel()
						if l == "" {
							return fmt.Errorf("toolchain_type field of DeclaredToolchainInfo of toolchain %#v is not a label", toolchainLabelStr)
						}
						toolchainType = &l
					case "use_target_platform_constraints":
						if !value.Message.HasBool() {
							return fmt.Errorf("use_target_platform_constraints field of DeclaredToolchainInfo of toolchain %#v is not a bool", toolchainLabelStr)
						}
						useTargetPlatformConstraints = value.Message.GetBool()
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
				execCompatibleWith, err := constraintValuesToConstraints(
					h.context,
					h.environment,
					h.valueReaders,
					toolchainPackage,
					ruleTarget.GetExecCompatibleWith(),
				)
				if err != nil {
					if !errors.Is(err, evaluation.ErrMissingDependency) {
						return err
					}
					*h.missingDependencies = true
				}

				// Annoyingly enough, target_compatible_with is
				// configurable. Expand select() expressions.
				// TODO: Is this using the right configuration?
				var targetCompatibleWithLabels []string
				for _, selectGroup := range ruleTarget.GetTargetCompatibleWith() {
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
					targetCompatibleWithList := targetCompatibleWithValue.GetList()
					if targetCompatibleWithList == nil {
						return fmt.Errorf("target_compatible_with of toolchain %#v is not a list", toolchainLabelStr)
					}

					var errIter error
					for element := range btree.AllLeaves(
						h.context,
						listReader,
						model_core.Nested(targetValue, targetCompatibleWithList.GetElements()),
						func(element model_core.Message[*model_starlark_pb.List_Element, TReference]) (*model_core_pb.DecodableReference, error) {
							return element.Message.GetParent().GetReference(), nil
						},
						&errIter,
					) {
						level := element.Message.GetLeaf()
						if level == nil {
							return fmt.Errorf("invalid list element level type for target_compatible_with of toolchain %#v", toolchainLabelStr)
						}
						l := level.GetLabel()
						if l == "" {
							return fmt.Errorf("invalid list element type for target_compatible_with of toolchain %#v", toolchainLabelStr)
						}
						targetCompatibleWithLabels = append(targetCompatibleWithLabels, l)
					}
					if errIter != nil {
						return err
					}
				}

				targetCompatibleWith, err := constraintValuesToConstraints(
					h.context,
					h.environment,
					h.valueReaders,
					toolchainPackage,
					targetCompatibleWithLabels,
				)
				if err != nil {
					if !errors.Is(err, evaluation.ErrMissingDependency) {
						return err
					}
					*h.missingDependencies = true
				}

				var compatibleWith *model_analysis_pb.RegisteredToolchain_CompatibleWith
				if useTargetPlatformConstraints {
					if len(execCompatibleWith) > 0 {
						return fmt.Errorf("exec_compatible_with of toolchain %#v is non-empty, while use_target_platform_constraints is set to True", toolchainLabelStr)
					}
					if len(targetCompatibleWith) > 0 {
						return fmt.Errorf("target_compatible_with of toolchain %#v is non-empty, while use_target_platform_constraints is set to True", toolchainLabelStr)
					}
				} else {
					compatibleWith = &model_analysis_pb.RegisteredToolchain_CompatibleWith{}
					compatibleWith.Exec = execCompatibleWith
					compatibleWith.Target = targetCompatibleWith
				}

				if !*h.missingDependencies {
					slices.Sort(targetSettings)
					h.registeredToolchainsByType[*toolchainType] = append(
						h.registeredToolchainsByType[*toolchainType], &model_analysis_pb.RegisteredToolchain{
							CompatibleWith: compatibleWith,
							TargetSettings: slices.Compact(targetSettings),
							Toolchain:      *toolchain,
						},
					)
				}
			}
			if iterErr != nil {
				if !errors.Is(err, evaluation.ErrMissingDependency) {
					return fmt.Errorf("failed to expand target pattern %#v: %w", canonicalToolchainTargetPattern.String(), iterErr)
				}
				*h.missingDependencies = true
			}
		}
	}
	return nil
}

func (registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) UseExtension(extensionBzlFile label.ApparentLabel, extensionName label.StarlarkIdentifier, devDependency, isolate bool) (dotbazel.ModuleExtensionProxy, error) {
	return dotbazel.NullModuleExtensionProxy, nil
}

func (registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]) UseRepoRule(repoRuleBzlFile label.ApparentLabel, repoRuleName label.StarlarkIdentifier) (dotbazel.RepoRuleProxy, error) {
	return func(thread *eval.Thread, name label.ApparentRepo, devDependency bool, attrs map[string]eval.Value) error {
		return nil
	}, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeRegisteredToolchainsValue(ctx context.Context, key *model_analysis_pb.RegisteredToolchains_Key, e RegisteredToolchainsEnvironment[TReference, TMetadata]) (PatchedRegisteredToolchainsValue[TMetadata], error) {
	analysisReaders, gotAnalysisReaders := getAnalysisReaders(e)
	valueReaders, gotValueReaders := getStarlarkValueReaders(e)

	registeredToolchainsByType := map[string][]*model_analysis_pb.RegisteredToolchain{}
	missingDependencies := false

	if !gotAnalysisReaders || !gotValueReaders {
		return PatchedRegisteredToolchainsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	if err := c.visitModuleDotBazelFilesBreadthFirst(ctx, e, func(moduleInstance label.ModuleInstance, ignoreDevDependencies bool) dotbazel.ChildHandler {
		return &registeredToolchainExtractingModuleDotBazelHandler[TReference, TMetadata]{
			context:                    ctx,
			computer:                   c,
			environment:                e,
			valueReaders:               valueReaders,
			analysisReaders:            analysisReaders,
			labelResolver:              newLabelResolver(e),
			moduleInstance:             moduleInstance,
			ignoreDevDependencies:      ignoreDevDependencies,
			registeredToolchainsByType: registeredToolchainsByType,
			missingDependencies:        &missingDependencies,
		}
	}); err != nil {
		return PatchedRegisteredToolchainsValue[TMetadata]{}, err
	}
	if missingDependencies {
		return PatchedRegisteredToolchainsValue[TMetadata]{}, evaluation.ErrMissingDependency
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
