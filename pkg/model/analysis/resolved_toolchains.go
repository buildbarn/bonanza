package analysis

import (
	"context"
	"fmt"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
)

var toolchainTypeInfoProviderIdentifier = util.Must(label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%ToolchainTypeInfo"))

func constraintsAreCompatible(actual, expected []*model_analysis_pb.Constraint) bool {
	for len(actual) > 0 && len(expected) > 0 {
		if cmp := strings.Compare(actual[0].Setting, expected[0].Setting); cmp < 0 {
			// Object has an additional constraint that we
			// don't care about. Ignore it.
			actual = actual[1:]
		} else if cmp > 0 {
			if expected[0].Value != "" {
				// Object lacks a constraint that we
				// care about.
				return false
			}
			// Object lacks a constraint for which it is
			// required that its value is equal to the
			// default value. This is acceptable.
			expected = expected[1:]
		} else {
			if expected[0].Value != actual[0].Value {
				// Object has a constraint whose value
				// differs from what is expected.
				return false
			}
			actual = actual[1:]
			expected = expected[1:]
		}
	}
	for _, c := range expected {
		if c.Value != "" {
			// Object lacks a constraint whose value has to
			// differ from the constraint's default value.
			return false
		}
	}
	return true
}

// getToolchainTypeNoMatchError returns the custom error message that
// was provided to the no_match_error attribute of toolchain_type(), in
// a form that can be appended to an error message. If no custom error
// message was provided, an empty string is returned.
//
// This function should only be called when toolchain resolution
// fails, so that the toolchain_type() target doesn't need to be
// configured in the common case.
func getToolchainTypeNoMatchError[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](ctx context.Context, e ResolvedToolchainsEnvironment[TReference, TMetadata], toolchainType string) (string, error) {
	valueReaders, gotValueReaders := getStarlarkValueReaders(e)
	fileReader, gotFileReader := getFileReader(e)
	if !gotValueReaders || !gotFileReader {
		return "", evaluation.ErrMissingDependency
	}

	// Similar to toolchain(), toolchain_type() is configured
	// without a configuration.
	toolchainTypeInfoProvider, err := getProviderFromConfiguredTarget(
		e,
		toolchainType,
		model_core.NewSimplePatchedMessage[TMetadata]((*model_core_pb.DecodableReference)(nil)),
		toolchainTypeInfoProviderIdentifier,
	)
	if err != nil {
		return "", fmt.Errorf("toolchain type %#v: %w", toolchainType, err)
	}
	noMatchErrorValue, err := model_starlark.GetStructFieldValue(ctx, valueReaders.List, toolchainTypeInfoProvider, "no_match_error")
	if err != nil {
		return "", fmt.Errorf("failed to obtain no_match_error field of ToolchainTypeInfo provider of toolchain type %#v: %w", toolchainType, err)
	}
	noMatchError, ok, err := getStringValue(ctx, fileReader, noMatchErrorValue)
	if err != nil {
		return "", fmt.Errorf("no_match_error field of ToolchainTypeInfo provider of toolchain type %#v: %w", toolchainType, err)
	}
	if !ok {
		return "", fmt.Errorf("no_match_error field of ToolchainTypeInfo provider of toolchain type %#v is not a string", toolchainType)
	}
	if noMatchError == "" {
		return "", nil
	}
	return ": " + noMatchError, nil
}

func (baseComputer[TReference, TMetadata]) ComputeResolvedToolchainsValue(ctx context.Context, key model_core.Message[*model_analysis_pb.ResolvedToolchains_Key, TReference], e ResolvedToolchainsEnvironment[TReference, TMetadata]) (PatchedResolvedToolchainsValue[TMetadata], error) {
	// Obtain all compatible execution platforms and toolchains.
	missingDependencies := false
	compatibleExecutionPlatforms := e.GetCompatibleExecutionPlatformsValue(&model_analysis_pb.CompatibleExecutionPlatforms_Key{
		Constraints: key.Message.ExecCompatibleWith,
	})
	if !compatibleExecutionPlatforms.IsSet() {
		missingDependencies = true
	}
	compatibleToolchainsByType := make([][]*model_analysis_pb.RegisteredToolchain, 0, len(key.Message.Toolchains))
	visibleToolchainTypes := make([]string, 0, len(key.Message.Toolchains))
	for _, toolchain := range key.Message.Toolchains {
		toolchainTypeLabel, err := label.NewCanonicalLabel(toolchain.ToolchainType)
		if err != nil {
			return PatchedResolvedToolchainsValue[TMetadata]{}, fmt.Errorf("invalid toolchain %#v label: %w", toolchain, err)
		}
		visibleToolchainType := e.GetVisibleTargetValue(
			model_core.NewSimplePatchedMessage[TMetadata](
				&model_analysis_pb.VisibleTarget_Key{
					FromPackage: toolchainTypeLabel.GetCanonicalPackage().String(),
					ToLabel:     toolchainTypeLabel.String(),
				},
			),
		)
		if !visibleToolchainType.IsSet() {
			missingDependencies = true
			continue
		}

		configurationReference := model_core.Patch(e, model_core.Nested(key, key.Message.ConfigurationReference))
		compatibleToolchainsForTypeValue := e.GetCompatibleToolchainsForTypeValue(
			model_core.NewPatchedMessage(
				&model_analysis_pb.CompatibleToolchainsForType_Key{
					ToolchainType:          visibleToolchainType.Message.Label,
					ConfigurationReference: configurationReference.Message,
				},
				configurationReference.Patcher,
			),
		)
		if !compatibleToolchainsForTypeValue.IsSet() {
			missingDependencies = true
			continue
		}

		compatibleToolchainsForType := compatibleToolchainsForTypeValue.Message.Toolchains
		if len(compatibleToolchainsForType) == 0 && toolchain.Mandatory {
			noMatchError, err := getToolchainTypeNoMatchError(ctx, e, visibleToolchainType.Message.Label)
			if err != nil {
				return PatchedResolvedToolchainsValue[TMetadata]{}, err
			}
			return PatchedResolvedToolchainsValue[TMetadata]{}, fmt.Errorf("dependency on toolchain type %#v is mandatory, but no toolchains exist that are compatible with the target%s", toolchain.ToolchainType, noMatchError)
		}
		compatibleToolchainsByType = append(compatibleToolchainsByType, compatibleToolchainsForType)
		visibleToolchainTypes = append(visibleToolchainTypes, visibleToolchainType.Message.Label)
	}
	if missingDependencies {
		return PatchedResolvedToolchainsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Pick the first execution platform having at least one
	// matching toolchain for all mandatory toolchain types.
	executionPlatforms := compatibleExecutionPlatforms.Message.ExecutionPlatforms
	toolchainTypeHasAtLeastOneMatchingExecutionPlatform := make([]bool, len(compatibleToolchainsByType))
CheckExecutionPlatform:
	for _, executionPlatform := range executionPlatforms {
		resolvedToolchains := make([]*model_analysis_pb.RegisteredToolchain, 0, len(compatibleToolchainsByType))
	CheckToolchainType:
		for i, toolchainsForType := range compatibleToolchainsByType {
			for _, toolchain := range toolchainsForType {
				compatibleWith := toolchain.CompatibleWith
				if compatibleWith == nil {
					// TODO: Add support for this.
					return PatchedResolvedToolchainsValue[TMetadata]{}, fmt.Errorf("toolchain %#v uses use_target_platform_constraints, which is not yet supported", toolchain.Toolchain)
				}
				if constraintsAreCompatible(executionPlatform.Constraints, compatibleWith.Exec) {
					toolchainTypeHasAtLeastOneMatchingExecutionPlatform[i] = true
					resolvedToolchains = append(resolvedToolchains, toolchain)
					continue CheckToolchainType
				}
			}

			// Did not find any compatible toolchain.
			if key.Message.Toolchains[i].Mandatory {
				continue CheckExecutionPlatform
			}
			toolchainTypeHasAtLeastOneMatchingExecutionPlatform[i] = true
			resolvedToolchains = append(resolvedToolchains, nil)
		}

		// Found an execution platform for which all mandatory
		// toolchain types have a compatible toolchain.
		//
		// The label stored in DeclaredToolchainInfo.toolchain
		// does not have aliases expanded, as that would in many
		// cases cause all toolchains to be downloaded,
		// regardless of their use. This means that alias
		// expansion needs to happen here.
		toolchainIdentifiers := make([]string, 0, len(resolvedToolchains))
		missingDependencies := false
		for _, resolvedToolchain := range resolvedToolchains {
			if resolvedToolchain == nil {
				// Optional toolchain that is missing.
				toolchainIdentifiers = append(toolchainIdentifiers, "")
			} else {
				resolvedToolchainLabel, err := label.NewCanonicalLabel(resolvedToolchain.Toolchain)
				if err != nil {
					return PatchedResolvedToolchainsValue[TMetadata]{}, fmt.Errorf("invalid toolchain label %#v: %w", resolvedToolchain.Toolchain, err)
				}
				visibleTarget := e.GetVisibleTargetValue(
					model_core.NewSimplePatchedMessage[TMetadata](
						&model_analysis_pb.VisibleTarget_Key{
							FromPackage: resolvedToolchainLabel.GetCanonicalPackage().String(),
							ToLabel:     resolvedToolchainLabel.String(),
							// Don't specify a configuration, as
							// the toolchain() itself is also
							// evaluated without one.
						},
					),
				)
				if !visibleTarget.IsSet() {
					missingDependencies = true
					continue
				}
				if !missingDependencies {
					toolchainIdentifiers = append(toolchainIdentifiers, visibleTarget.Message.Label)
				}
			}
		}
		if missingDependencies {
			return PatchedResolvedToolchainsValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ResolvedToolchains_Value{
			ToolchainIdentifiers:  toolchainIdentifiers,
			PlatformLabel:         executionPlatform.Label,
			PlatformPkixPublicKey: executionPlatform.ExecPkixPublicKey,
		}), nil
	}

	for i, hasMatching := range toolchainTypeHasAtLeastOneMatchingExecutionPlatform {
		if !hasMatching {
			noMatchError, err := getToolchainTypeNoMatchError(ctx, e, visibleToolchainTypes[i])
			if err != nil {
				return PatchedResolvedToolchainsValue[TMetadata]{}, err
			}
			return PatchedResolvedToolchainsValue[TMetadata]{}, fmt.Errorf("dependency on toolchain type %#v is mandatory, but none of the %d toolchains that are compatible with the target are also compatible with any of the %d execution platforms%s", key.Message.Toolchains[i].ToolchainType, len(compatibleToolchainsByType[i]), len(executionPlatforms), noMatchError)
		}
	}
	return PatchedResolvedToolchainsValue[TMetadata]{}, fmt.Errorf("even though all mandatory toolchain types have at least one toolchain that is compatible with one of the %d execution platforms, no single execution platform exists for which all mandatory toolchain types have at least one compatible toolchain", len(executionPlatforms))
}

// ResolvedToolchainsEnvironmentForTesting is used to generate mocks for
// testing ComputeResolvedToolchainsValue().
type ResolvedToolchainsEnvironmentForTesting ResolvedToolchainsEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
