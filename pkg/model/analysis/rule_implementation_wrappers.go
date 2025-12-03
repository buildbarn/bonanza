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
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/starlark"
)

// RuleImplementationWrappers contains the Starlark functions that need
// to be invoked prior to running rules and subrules in order to wrap
// ctx and subrule_ctx objects. This can be used to add support for
// legacy features and improve compatibility with Bazel.
type RuleImplementationWrappers struct {
	Rule    starlark.Value
	Subrule starlark.Value
}

func getImplementationWrapper[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e RuleImplementationWrappersEnvironment[TReference, TMetadata],
	identifier string,
) (starlark.Value, error) {
	if identifier == "" {
		// No wrapper function configured. Just invoke the
		// underlying implementation function directly.
		return starlark.NewBuiltin("default_rule_implementation_wrapper", func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			return starlark.Call(thread, args[0], args[1:], kwargs)
		}), nil
	}

	wrapperValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
		Identifier: identifier,
	})
	if !wrapperValue.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}

	wrapperFunction, ok := wrapperValue.Message.Global.GetKind().(*model_starlark_pb.Value_Function)
	if !ok {
		return nil, errors.New("global value is not a function")
	}
	return model_starlark.NewNamedFunction(
		model_starlark.NewProtoNamedFunctionDefinition[TReference, TMetadata](
			model_core.Nested(wrapperValue, wrapperFunction.Function),
		),
	), nil
}

func (baseComputer[TReference, TMetadata]) ComputeRuleImplementationWrappersValue(ctx context.Context, key *model_analysis_pb.RuleImplementationWrappers_Key, e RuleImplementationWrappersEnvironment[TReference, TMetadata]) (*RuleImplementationWrappers, error) {
	buildSpecificationValue := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecificationValue.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	buildSpecification := buildSpecificationValue.Message

	ruleImplementationWrapper, err := getImplementationWrapper(e, buildSpecification.RuleImplementationWrapperIdentifier)
	if err != nil {
		return nil, fmt.Errorf("failed to get rule implementation wrapper: %w", err)
	}
	subruleImplementationWrapper, err := getImplementationWrapper(e, buildSpecification.SubruleImplementationWrapperIdentifier)
	if err != nil {
		return nil, fmt.Errorf("failed to get subrule implementation wrapper: %w", err)
	}
	return &RuleImplementationWrappers{
		Rule:    ruleImplementationWrapper,
		Subrule: subruleImplementationWrapper,
	}, nil
}
