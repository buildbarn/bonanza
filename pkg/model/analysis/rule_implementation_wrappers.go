package analysis

import (
	"context"
	"errors"
	"fmt"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
)

// getImplementationWrapper looks up the Starlark function that needs
// to be invoked prior to running rules or subrules in order to wrap
// ctx and subrule_ctx objects. This can be used to add support for
// legacy features and improve compatibility with Bazel. If no wrapper
// is configured, an unset message is returned.
func getImplementationWrapper[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e RuleImplementationWrappersEnvironment[TReference, TMetadata],
	identifier string,
) (model_core.Message[*model_starlark_pb.Function, TReference], error) {
	if identifier == "" {
		// No wrapper function configured.
		return model_core.Message[*model_starlark_pb.Function, TReference]{}, nil
	}

	wrapperValue := e.GetCompiledBzlFileGlobalValue(model_analysis_pb.CompiledBzlFileGlobal_Key_builder{
		Identifier: identifier,
	}.Build())
	if !wrapperValue.IsSet() {
		return model_core.Message[*model_starlark_pb.Function, TReference]{}, evaluation.ErrMissingDependency
	}

	wrapperFunction := wrapperValue.Message.GetGlobal().GetFunction()
	if wrapperFunction == nil {
		return model_core.Message[*model_starlark_pb.Function, TReference]{}, errors.New("global value is not a function")
	}
	return model_core.Nested(wrapperValue, wrapperFunction), nil
}

func (baseComputer[TReference, TMetadata]) ComputeRuleImplementationWrappersValue(ctx context.Context, key *model_analysis_pb.RuleImplementationWrappers_Key, e RuleImplementationWrappersEnvironment[TReference, TMetadata]) (PatchedRuleImplementationWrappersValue[TMetadata], error) {
	identifiersValue := e.GetRuleImplementationWrappersIdentifiersValue(&model_analysis_pb.RuleImplementationWrappersIdentifiers_Key{})
	if !identifiersValue.IsSet() {
		return PatchedRuleImplementationWrappersValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	identifiers := identifiersValue.Message

	// Look up all wrappers before patching any messages, so that
	// no metadata is leaked if any of them is unavailable.
	ruleImplementationWrapper, ruleErr := getImplementationWrapper(e, identifiers.GetRuleIdentifier())
	subruleImplementationWrapper, subruleErr := getImplementationWrapper(e, identifiers.GetSubruleIdentifier())
	aspectImplementationWrapper, aspectErr := getImplementationWrapper(e, identifiers.GetAspectIdentifier())
	if ruleErr != nil {
		return PatchedRuleImplementationWrappersValue[TMetadata]{}, fmt.Errorf("failed to get rule implementation wrapper: %w", ruleErr)
	}
	if subruleErr != nil {
		return PatchedRuleImplementationWrappersValue[TMetadata]{}, fmt.Errorf("failed to get subrule implementation wrapper: %w", subruleErr)
	}
	if aspectErr != nil {
		return PatchedRuleImplementationWrappersValue[TMetadata]{}, fmt.Errorf("failed to get aspect implementation wrapper: %w", aspectErr)
	}

	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.RuleImplementationWrappers_Value {
		value := &model_analysis_pb.RuleImplementationWrappers_Value{}
		if ruleImplementationWrapper.IsSet() {
			value.SetRule(model_core.Patch(e, ruleImplementationWrapper).Merge(patcher))
		}
		if subruleImplementationWrapper.IsSet() {
			value.SetSubrule(model_core.Patch(e, subruleImplementationWrapper).Merge(patcher))
		}
		if aspectImplementationWrapper.IsSet() {
			value.SetAspect(model_core.Patch(e, aspectImplementationWrapper).Merge(patcher))
		}
		return value
	}), nil
}

func (baseComputer[TReference, TMetadata]) ComputeRuleImplementationWrappersIdentifiersValue(ctx context.Context, key *model_analysis_pb.RuleImplementationWrappersIdentifiers_Key, e RuleImplementationWrappersIdentifiersEnvironment[TReference, TMetadata]) (PatchedRuleImplementationWrappersIdentifiersValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedRuleImplementationWrappersIdentifiersValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](model_analysis_pb.RuleImplementationWrappersIdentifiers_Value_builder{
		RuleIdentifier:    buildSpecification.Message.GetRuleImplementationWrapperIdentifier(),
		SubruleIdentifier: buildSpecification.Message.GetSubruleImplementationWrapperIdentifier(),
		AspectIdentifier:  buildSpecification.Message.GetAspectImplementationWrapperIdentifier(),
	}.Build()), nil
}

// defaultImplementationWrapper is used if no implementation wrapper is
// configured. It invokes the implementation function directly.
var defaultImplementationWrapper = eval.NewBuiltin("default_rule_implementation_wrapper", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	if len(args.Positional) < 1 {
		return nil, fmt.Errorf("%s() requires the implementation function as its first argument", b.Name())
	}
	return eval.Call(thread, args.Positional[0], eval.Args{Positional: args.Positional[1:], Named: args.Named})
})

// decodeImplementationWrapper converts an implementation wrapper that
// is stored in a RuleImplementationWrappers value to a Starlark value
// that can be called.
func decodeImplementationWrapper[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](wrapper model_core.Message[*model_starlark_pb.Function, TReference]) eval.Value {
	if wrapper.Message == nil {
		return defaultImplementationWrapper
	}
	return model_starlark.DecodeFunction[TReference, TMetadata](wrapper)
}

// RuleImplementationWrappersEnvironmentForTesting is used to generate
// mocks for testing ComputeRuleImplementationWrappersValue().
type RuleImplementationWrappersEnvironmentForTesting RuleImplementationWrappersEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
