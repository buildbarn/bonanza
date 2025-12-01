package starlark

import (
	"errors"
	"fmt"
	"sync/atomic"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// NamedFunction is a wrapper around starlark-go's own
// *starlark.Function that supports lazily reloading functions from
// storage when they get called.
type NamedFunction[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	NamedFunctionDefinition[TReference, TMetadata]
}

var _ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*NamedFunction[object.LocalReference, model_core.ReferenceMetadata])(nil)

// NewNamedFunction creates a function object, either by wrapping a
// function returned by starlark-go, or by using a Protobuf message
// containing a definition of the function that is backed by storage.
func NewNamedFunction[TReference any, TMetadata model_core.ReferenceMetadata](definition NamedFunctionDefinition[TReference, TMetadata]) NamedFunction[TReference, TMetadata] {
	return NamedFunction[TReference, TMetadata]{
		NamedFunctionDefinition: definition,
	}
}

func (f NamedFunction[TReference, TMetadata]) String() string {
	return fmt.Sprintf("<function %s>", f.Name())
}

// Type returns the type name of a function value as a string.
func (NamedFunction[TReference, TMetadata]) Type() string {
	return "function"
}

// Freeze a function, so that it becomes immutable.
func (NamedFunction[TReference, TMetadata]) Freeze() {
	// TODO: Should we call into the NamedFunctionDefinition to
	// actually freeze the function?
}

// Truth returns whether the function is "truthy" or "falsy". Functions
// are always "truthy".
func (NamedFunction[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

// Hash a function, so that it can be placed in a set or be used as a
// key in a dict. Functions cannot be hashed.
func (NamedFunction[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("function cannot be hashed")
}

// EncodeValue encodes a function to a Starlark value Protobuf message,
// so that it can be written to storage. When the function is called
// from another .bzl file, it can be reloaded and invoked again.
func (f NamedFunction[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	function, needsCode, err := f.NamedFunctionDefinition.Encode(path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Function{
				Function: function.Message,
			},
		},
		function.Patcher,
	), needsCode, nil
}

// NamedFunctionDefinition is called into by NamedFunction to obtain
// properties of a function that either resides in memory or is still
// backed by storage. Properties include the name of the function, the
// file and line at which the function is declared, and the number of
// parameters it has.
//
// NamedFunctionDefinition can also be used to invoke the underlying
// function. When the function resides in storage, it is first loaded
// into memory.
type NamedFunctionDefinition[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error)
	Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata], bool, error)
	Name() string
	Position() syntax.Position
	NumParams(thread *starlark.Thread) (int, error)
}

type starlarkNamedFunctionDefinition[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	*starlark.Function
}

// NewStarlarkNamedFunctionDefinition creates a NamedFunctionDefinition
// that is backed by a Starlark function of a file that has been parsed
// and compiled.
func NewStarlarkNamedFunctionDefinition[TReference any, TMetadata model_core.ReferenceMetadata](function *starlark.Function) NamedFunctionDefinition[TReference, TMetadata] {
	return starlarkNamedFunctionDefinition[TReference, TMetadata]{
		Function: function,
	}
}

func (d starlarkNamedFunctionDefinition[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata], bool, error) {
	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
	position := d.Function.Position()
	filename := position.Filename()
	needsCode := options.CurrentFilename != nil && filename == options.CurrentFilename.String()
	name := d.Function.Name()

	var closure *model_starlark_pb.Function_Closure
	if position.Col != 1 || name == "lambda" {
		if _, ok := path[d]; ok {
			return model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata]{}, false, errors.New("value is defined recursively")
		}
		path[d] = struct{}{}
		defer delete(path, d)

		numRawDefaults := d.Function.NumRawDefaults()
		defaultParameters := make([]*model_starlark_pb.Function_Closure_DefaultParameter, 0, numRawDefaults)
		for index := 0; index < numRawDefaults; index++ {
			if defaultValue := d.Function.RawDefault(index); defaultValue != nil {
				encodedDefaultValue, defaultValueNeedsCode, err := EncodeValue[TReference, TMetadata](defaultValue, path, nil, options)
				if err != nil {
					return model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata]{}, false, fmt.Errorf("default parameter %d: %w", index, err)
				}
				defaultParameters = append(defaultParameters, &model_starlark_pb.Function_Closure_DefaultParameter{
					Value: encodedDefaultValue.Message,
				})
				patcher.Merge(encodedDefaultValue.Patcher)
				needsCode = needsCode || defaultValueNeedsCode
			} else {
				defaultParameters = append(defaultParameters, &model_starlark_pb.Function_Closure_DefaultParameter{})
			}
		}

		numFreeVars := d.Function.NumFreeVars()
		freeVars := make([]*model_starlark_pb.Value, 0, numFreeVars)
		for index := 0; index < numFreeVars; index++ {
			_, freeVar := d.Function.FreeVar(index)
			encodedFreeVar, freeVarNeedsCode, err := EncodeValue[TReference, TMetadata](freeVar, path, nil, options)
			if err != nil {
				return model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata]{}, false, fmt.Errorf("free variable %d: %w", index, err)
			}
			freeVars = append(freeVars, encodedFreeVar.Message)
			patcher.Merge(encodedFreeVar.Patcher)
			needsCode = needsCode || freeVarNeedsCode
		}

		closure = &model_starlark_pb.Function_Closure{
			Index:             d.Function.Index(),
			DefaultParameters: defaultParameters,
			FreeVariables:     freeVars,
		}
	}

	return model_core.NewPatchedMessage(
		&model_starlark_pb.Function{
			Filename: filename,
			Line:     position.Line,
			Column:   position.Col,
			Name:     d.Function.Name(),
			Closure:  closure,
		},
		patcher,
	), needsCode, nil
}

func (d starlarkNamedFunctionDefinition[TReference, TMetadata]) NumParams(thread *starlark.Thread) (int, error) {
	return d.Function.NumParams(), nil
}

// FunctionFactoryResolver is called into by the NamedFunctionDefinition
// created by NewProtoNamedFunctionDefinition() when a call is made to
// the function.
//
// The FunctionFactoryResolver is responsible for returning a Starlark
// FunctionFactory that, for the provided Starlark file, is capable of
// recreating Starlark function objects.
type FunctionFactoryResolver = func(filename pg_label.CanonicalLabel) (*starlark.FunctionFactory, error)

// FunctionFactoryResolverKey is the key under which a
// FunctionFactoryResolver should be placed in the thread local
// variables of a Starlark thread.
const FunctionFactoryResolverKey = "function_factory_resolver"

type protoNamedFunctionDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	message  model_core.Message[*model_starlark_pb.Function, TReference]
	function atomic.Pointer[starlark.Function]
}

// NewProtoNamedFunctionDefinition creates a NamedFunctionDefinition
// that is backed by a function reference that is backed by a Protobuf
// message in storage. The function is reloaded from storage when
// called.
func NewProtoNamedFunctionDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](message model_core.Message[*model_starlark_pb.Function, TReference]) NamedFunctionDefinition[TReference, TMetadata] {
	return &protoNamedFunctionDefinition[TReference, TMetadata]{
		message: message,
	}
}

func (d *protoNamedFunctionDefinition[TReference, TMetadata]) getFunction(thread *starlark.Thread) (*starlark.Function, error) {
	// TODO: We should remove the caching, as it causes dependencies
	// to be unstable.
	function := d.function.Load()
	if function == nil {
		functionFactoryResolver := thread.Local(FunctionFactoryResolverKey)
		if functionFactoryResolver == nil {
			return nil, errors.New("indirect functions cannot be resolved from within this context")
		}
		definition := d.message.Message
		if definition == nil {
			return nil, errors.New("no function message present")
		}
		filename, err := pg_label.NewCanonicalLabel(definition.Filename)
		if err != nil {
			return nil, fmt.Errorf("invalid filename %#v: %w", definition.Filename, err)
		}
		functionFactory, err := functionFactoryResolver.(FunctionFactoryResolver)(filename)
		if err != nil {
			return nil, err
		}

		if closure := definition.Closure; closure == nil {
			function, err = functionFactory.NewFunctionByName(definition.Name)
			if err != nil {
				return nil, err
			}
		} else {
			options := thread.Local(ValueDecodingOptionsKey).(*ValueDecodingOptions[TReference])
			freeVariables := make(starlark.Tuple, 0, len(closure.FreeVariables))
			for index, freeVariable := range closure.FreeVariables {
				value, err := DecodeValue[TReference, TMetadata](model_core.Nested(d.message, freeVariable), nil, options)
				if err != nil {
					return nil, fmt.Errorf("invalid free variable %d: %w", index, err)
				}
				freeVariables = append(freeVariables, value)
			}

			defaultParameters := make(starlark.Tuple, len(closure.DefaultParameters))
			for index, defaultParameter := range closure.DefaultParameters {
				if defaultParameter.Value != nil {
					value, err := DecodeValue[TReference, TMetadata](model_core.Nested(d.message, defaultParameter.Value), nil, options)
					if err != nil {
						return nil, fmt.Errorf("invalid default parameter %d: %w", index, err)
					}
					defaultParameters[index] = value
				}
			}

			function, err = functionFactory.NewFunctionByIndex(closure.Index, defaultParameters, freeVariables)
			if err != nil {
				return nil, err
			}
		}

		d.function.Store(function)
	}
	return function, nil
}

func (d *protoNamedFunctionDefinition[TReference, TMetadata]) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	function, err := d.getFunction(thread)
	if err != nil {
		return nil, err
	}
	return function.CallInternal(thread, args, kwargs)
}

func (d *protoNamedFunctionDefinition[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata], bool, error) {
	return model_core.Patch(options.ObjectCapturer, d.message), false, nil
}

func (d *protoNamedFunctionDefinition[TReference, TMetadata]) Name() string {
	if m := d.message.Message; m != nil {
		return m.Name
	}
	return "unknown"
}

func (d *protoNamedFunctionDefinition[TReference, TMetadata]) Position() syntax.Position {
	if m := d.message.Message; m != nil {
		return syntax.MakePosition(&m.Filename, m.Line, m.Column)
	}
	return syntax.MakePosition(nil, 0, 0)
}

func (d *protoNamedFunctionDefinition[TReference, TMetadata]) NumParams(thread *starlark.Thread) (int, error) {
	function, err := d.getFunction(thread)
	if err != nil {
		return 0, err
	}
	return function.NumParams(), nil
}

type namedFunctionUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata] struct{}

// NewNamedFunctionUnpackerInto creates a Starlark function argument
// unpacker that can unpack arguments that need to correspond to named
// functions.
func NewNamedFunctionUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata]() unpack.UnpackerInto[NamedFunction[TReference, TMetadata]] {
	return namedFunctionUnpackerInto[TReference, TMetadata]{}
}

func (namedFunctionUnpackerInto[TReference, TMetadata]) UnpackInto(thread *starlark.Thread, v starlark.Value, dst *NamedFunction[TReference, TMetadata]) error {
	switch typedV := v.(type) {
	case *starlark.Function:
		*dst = NewNamedFunction(NewStarlarkNamedFunctionDefinition[TReference, TMetadata](typedV))
		return nil
	case NamedFunction[TReference, TMetadata]:
		*dst = typedV
		return nil
	default:
		return fmt.Errorf("got %s, want function", v.Type())
	}
}

func (ui namedFunctionUnpackerInto[TReference, TMetadata]) Canonicalize(thread *starlark.Thread, v starlark.Value) (starlark.Value, error) {
	var f NamedFunction[TReference, TMetadata]
	if err := ui.UnpackInto(thread, v, &f); err != nil {
		return nil, err
	}
	return f, nil
}

func (namedFunctionUnpackerInto[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return 0
}
