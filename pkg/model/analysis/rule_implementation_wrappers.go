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

	wrapperValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
		Identifier: identifier,
	})
	if !wrapperValue.IsSet() {
		return model_core.Message[*model_starlark_pb.Function, TReference]{}, evaluation.ErrMissingDependency
	}

	wrapperFunction, ok := wrapperValue.Message.Global.GetKind().(*model_starlark_pb.Value_Function)
	if !ok {
		return model_core.Message[*model_starlark_pb.Function, TReference]{}, errors.New("global value is not a function")
	}
	return model_core.Nested(wrapperValue, wrapperFunction.Function), nil
}

func (baseComputer[TReference, TMetadata]) ComputeRuleImplementationWrappersValue(ctx context.Context, key *model_analysis_pb.RuleImplementationWrappers_Key, e RuleImplementationWrappersEnvironment[TReference, TMetadata]) (PatchedRuleImplementationWrappersValue[TMetadata], error) {
	buildSpecificationValue := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecificationValue.IsSet() {
		return PatchedRuleImplementationWrappersValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	buildSpecification := buildSpecificationValue.Message

	// Look up both wrappers before patching any messages, so that
	// no metadata is leaked if either of them is unavailable.
	ruleImplementationWrapper, ruleErr := getImplementationWrapper(e, buildSpecification.RuleImplementationWrapperIdentifier)
	subruleImplementationWrapper, subruleErr := getImplementationWrapper(e, buildSpecification.SubruleImplementationWrapperIdentifier)
	if ruleErr != nil {
		return PatchedRuleImplementationWrappersValue[TMetadata]{}, fmt.Errorf("failed to get rule implementation wrapper: %w", ruleErr)
	}
	if subruleErr != nil {
		return PatchedRuleImplementationWrappersValue[TMetadata]{}, fmt.Errorf("failed to get subrule implementation wrapper: %w", subruleErr)
	}

	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.RuleImplementationWrappers_Value {
		value := &model_analysis_pb.RuleImplementationWrappers_Value{}
		if ruleImplementationWrapper.IsSet() {
			value.Rule = model_core.Patch(e, ruleImplementationWrapper).Merge(patcher)
		}
		if subruleImplementationWrapper.IsSet() {
			value.Subrule = model_core.Patch(e, subruleImplementationWrapper).Merge(patcher)
		}
		return value
	}), nil
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
