package starlark

import (
	"errors"
	"fmt"
	"math"
	"math/big"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
)

// encodableFunction is implemented by functions that are backed by
// storage, which can be encoded by returning their original message.
type encodableFunction[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	encodeFunction(options *ValueEncodingOptions[TReference, TMetadata]) model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata]
}

// EncodeFunction encodes a Starlark function to a Function message, so
// that it can be written to storage. The function is referred to by
// name, as its code is stored in the CompiledProgram of the file that
// declares it. This ensures that the resulting message only changes if
// the name or closure of the function changes. Values of default
// parameters and free variables are stored in the message itself.
func EncodeFunction[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, function eval.Function, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata], error) {
	if encodable, ok := function.(encodableFunction[TReference, TMetadata]); ok {
		return encodable.encodeFunction(options), nil
	}

	for _, v := range path {
		if v == eval.Value(function) {
			return model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata]{}, errors.New("value is defined recursively")
		}
	}
	path = append(path, function)

	module, err := function.Module(thread)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata]{}, err
	}
	namePath := function.NamePath()
	if len(namePath) == 0 {
		return model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata]{}, errors.New("function has no name path")
	}
	encodedNamePath := make([]*model_starlark_pb.Function_Name, 0, len(namePath))
	for _, name := range namePath {
		encodedNamePath = append(encodedNamePath, encodeFunctionName(name))
	}
	defaults, err := function.Defaults(thread)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata]{}, err
	}
	freeVars, err := function.FreeVars(thread)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata]{}, err
	}

	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
	var closure *model_starlark_pb.Function_Closure
	if len(defaults) > 0 || len(freeVars) > 0 {
		defaultParameters := make([]*model_starlark_pb.Function_Closure_DefaultParameter, 0, len(defaults))
		for index, defaultValue := range defaults {
			defaultParameter := &model_starlark_pb.Function_Closure_DefaultParameter{}
			if defaultValue != eval.Mandatory {
				encodedDefaultValue, err := EncodeValue(thread, defaultValue, path, nil, options)
				if err != nil {
					patcher.Discard()
					return model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata]{}, fmt.Errorf("default parameter %d: %w", index, err)
				}
				defaultParameter.SetValue(encodedDefaultValue.Merge(patcher))
			}
			defaultParameters = append(defaultParameters, defaultParameter)
		}
		freeVariables := make([]*model_starlark_pb.Value, 0, len(freeVars))
		for index, freeVar := range freeVars {
			encodedFreeVar, err := EncodeValue(thread, freeVar.Value(), path, nil, options)
			if err != nil {
				patcher.Discard()
				return model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata]{}, fmt.Errorf("free variable %d: %w", index, err)
			}
			freeVariables = append(freeVariables, encodedFreeVar.Merge(patcher))
		}
		closure = model_starlark_pb.Function_Closure_builder{
			DefaultParameters: defaultParameters,
			FreeVariables:     freeVariables,
		}.Build()
	}

	return model_core.NewPatchedMessage(
		model_starlark_pb.Function_builder{
			Filename: module.Filename(),
			NamePath: encodedNamePath,
			Closure:  closure,
		}.Build(),
		patcher,
	), nil
}

func encodeFunctionValue[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, function eval.Function, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	encodedFunction, err := EncodeFunction(thread, function, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		model_starlark_pb.Value_builder{
			Function: proto.ValueOrDefault(encodedFunction.Message),
		}.Build(),
		encodedFunction.Patcher,
	), nil
}

// encodeFuncode converts the code of a function to a Funcode message.
// The code of nested functions is stored inside the message.
func encodeFuncode(funcode *compile.Funcode) (*model_starlark_pb.Funcode, error) {
	stringConstants := make([][]byte, 0, len(funcode.StringConstants))
	for _, constant := range funcode.StringConstants {
		stringConstants = append(stringConstants, []byte(constant))
	}
	intConstants := make([][]byte, 0, len(funcode.IntConstants))
	for _, constant := range funcode.IntConstants {
		intConstants = append(intConstants, EncodeInt(constant))
	}
	imports := make([]string, 0, len(funcode.Imports))
	for _, imp := range funcode.Imports {
		filename, err := pg_label.NewCanonicalLabel(imp.Module)
		if err != nil {
			return nil, fmt.Errorf("module %#v of loaded symbol %#v is not a canonical label: %w", imp.Module, imp.Name, err)
		}
		identifier, err := pg_label.NewStarlarkIdentifier(imp.Name)
		if err != nil {
			return nil, fmt.Errorf("loaded symbol %#v of module %#v is not a valid identifier: %w", imp.Name, imp.Module, err)
		}
		imports = append(imports, filename.AppendStarlarkIdentifier(identifier).String())
	}
	cells := make([]uint32, 0, len(funcode.Cells))
	for _, cell := range funcode.Cells {
		cells = append(cells, uint32(cell))
	}

	functions := make([]*model_starlark_pb.Funcode_NestedFunction, 0, len(funcode.Functions))
	for i, nestedFuncode := range funcode.Functions {
		encodedNestedFuncode, err := encodeFuncode(nestedFuncode)
		if err != nil {
			return nil, err
		}
		functions = append(functions, model_starlark_pb.Funcode_NestedFunction_builder{
			Name:         funcode.GetNestedFunctionName(i).Name,
			Funcode:      encodedNestedFuncode,
			RelativeLine: uint32(nestedFuncode.Pos.Line - funcode.Pos.Line),
		}.Build())
	}
	return model_starlark_pb.Funcode_builder{
		Column:               uint32(funcode.Pos.Column),
		Code:                 funcode.Code,
		LineTable:            funcode.LineTable,
		StringConstants:      stringConstants,
		IntConstants:         intConstants,
		FloatConstants:       funcode.FloatConstants,
		Names:                funcode.Names,
		Imports:              imports,
		Functions:            functions,
		Locals:               funcode.Locals,
		Cells:                cells,
		FreeVariables:        funcode.FreeVars,
		NumParams:            uint32(funcode.NumParams),
		NumKeywordOnlyParams: uint32(funcode.NumKeywordOnlyParams),
		HasArgs:              funcode.HasArgs,
		HasKwargs:            funcode.HasKwargs,
		MaxStack:             uint32(funcode.MaxStack),
	}.Build(), nil
}

func encodeFunctionName(name compile.FunctionName) *model_starlark_pb.Function_Name {
	return model_starlark_pb.Function_Name_builder{
		Name:                name.Name,
		DisambiguationIndex: int32(name.DisambiguationIndex),
	}.Build()
}

func decodeFunctionName(name *model_starlark_pb.Function_Name) compile.FunctionName {
	return compile.FunctionName{
		Name:                name.GetName(),
		DisambiguationIndex: int(name.GetDisambiguationIndex()),
	}
}

// CompiledProgramResolver is called to obtain the CompiledProgram of a
// .bzl file, which contains the code of the functions declared in it.
// In the context of an evaluation, this is typically implemented by
// requesting the CompiledBzlFile key.
type CompiledProgramResolver[TReference any] = func(filename pg_label.CanonicalLabel) (model_core.Message[*model_starlark_pb.CompiledProgram, TReference], error)

// CompiledProgramResolverKey is the key under which an instance of
// CompiledProgramResolver is stored in the thread local variables of a
// Starlark thread.
const CompiledProgramResolverKey = "compiled_program_resolver"

// topLevelFuncodeCacheKey is the key of a thread local variable that
// holds the code of top-level functions that have been decoded.
type topLevelFuncodeCacheKey struct{}

type topLevelFuncodeCacheEntryKey struct {
	filename pg_label.CanonicalLabel
	name     compile.FunctionName
}

// getTopLevelFuncode returns the code of a function that is declared at
// the top level of a file, including the code of functions nested
// inside it.
func getTopLevelFuncode[TReference object.BasicReference](thread *eval.Thread, filename pg_label.CanonicalLabel, name compile.FunctionName) (*compile.Funcode, error) {
	cache, _ := thread.Local(topLevelFuncodeCacheKey{}).(map[topLevelFuncodeCacheEntryKey]*compile.Funcode)
	if cache == nil {
		cache = map[topLevelFuncodeCacheEntryKey]*compile.Funcode{}
		thread.SetLocal(topLevelFuncodeCacheKey{}, cache)
	}
	cacheKey := topLevelFuncodeCacheEntryKey{filename: filename, name: name}
	if funcode, ok := cache[cacheKey]; ok {
		return funcode, nil
	}

	options, err := getValueDecodingOptions[TReference](thread)
	if err != nil {
		return nil, err
	}
	resolver, ok := thread.Local(CompiledProgramResolverKey).(CompiledProgramResolver[TReference])
	if !ok {
		return nil, errors.New("code of functions cannot be resolved from within this context")
	}
	compiledProgram, err := resolver(filename)
	if err != nil {
		return nil, err
	}

	index := uint64(0)
	entry, err := btree.FindWithPredecessors(
		thread.Context(),
		options.Readers.CompiledProgramFunctions,
		model_core.Nested(compiledProgram, compiledProgram.Message.GetFunctions()),
		func(entry model_core.Message[*model_starlark_pb.CompiledProgram_FunctionEntry, TReference]) (int, *model_core_pb.DecodableReference) {
			switch entry.Message.WhichLevel() {
			case model_starlark_pb.CompiledProgram_FunctionEntry_Leaf_case:
				return name.Compare(decodeFunctionName(entry.Message.GetLeaf().GetName())), nil
			case model_starlark_pb.CompiledProgram_FunctionEntry_Parent_case:
				return name.Compare(decodeFunctionName(entry.Message.GetParent().GetFirstName())), entry.Message.GetParent().GetReference()
			default:
				return 0, nil
			}
		},
		func(predecessors model_core.Message[[]*model_starlark_pb.CompiledProgram_FunctionEntry, TReference]) {
			for _, predecessor := range predecessors.Message {
				index += getFunctionEntryCount(predecessor)
			}
		},
	)
	if err != nil {
		return nil, err
	}
	code := entry.Message.GetLeaf().GetCode()
	if code == nil {
		return nil, fmt.Errorf("code of function does not exist in file %#v", filename.String())
	}
	lines := compiledProgram.Message.GetTopLevelFunctionLines()
	if index >= uint64(len(lines)) {
		return nil, fmt.Errorf("line of function does not exist in file %#v", filename.String())
	}
	funcode, err := DecodeFuncode(code, name.Name, int32(lines[index]))
	if err != nil {
		return nil, err
	}
	cache[cacheKey] = funcode
	return funcode, nil
}

// getFunctionEntryCount returns the number of functions that are
// contained in an entry of the B-tree of functions.
func getFunctionEntryCount(entry *model_starlark_pb.CompiledProgram_FunctionEntry) uint64 {
	if parent := entry.GetParent(); parent != nil {
		return uint64(parent.GetFunctionCount())
	}
	return 1
}

// DecodeFuncode converts a Funcode message to a compile.Funcode. As the
// message does not contain the name of the function and the line at
// which it is declared, these need to be provided by the caller. The
// name is empty for lambdas.
func DecodeFuncode(m *model_starlark_pb.Funcode, name string, line int32) (*compile.Funcode, error) {
	if name == "" {
		name = compile.LambdaName
	}
	stringConstants := m.GetStringConstants()
	intConstants := m.GetIntConstants()
	imports := m.GetImports()
	functions := m.GetFunctions()
	cells := m.GetCells()
	funcode := &compile.Funcode{
		Name:                 name,
		Pos:                  syntax.Position{Line: line, Column: int32(m.GetColumn())},
		Code:                 m.GetCode(),
		LineTable:            m.GetLineTable(),
		StringConstants:      make([]string, 0, len(stringConstants)),
		IntConstants:         make([]*big.Int, 0, len(intConstants)),
		FloatConstants:       m.GetFloatConstants(),
		Names:                m.GetNames(),
		Imports:              make([]compile.Import, 0, len(imports)),
		Functions:            make([]*compile.Funcode, 0, len(functions)),
		Locals:               m.GetLocals(),
		Cells:                make([]int, 0, len(cells)),
		FreeVars:             m.GetFreeVariables(),
		NumParams:            int(m.GetNumParams()),
		NumKeywordOnlyParams: int(m.GetNumKeywordOnlyParams()),
		HasArgs:              m.GetHasArgs(),
		HasKwargs:            m.GetHasKwargs(),
		MaxStack:             int(m.GetMaxStack()),
	}
	for _, constant := range stringConstants {
		funcode.StringConstants = append(funcode.StringConstants, string(constant))
	}
	for _, constant := range intConstants {
		funcode.IntConstants = append(funcode.IntConstants, DecodeInt(constant))
	}
	for _, imp := range imports {
		identifier, err := pg_label.NewCanonicalStarlarkIdentifier(imp)
		if err != nil {
			return nil, fmt.Errorf("invalid import %#v: %w", imp, err)
		}
		funcode.Imports = append(funcode.Imports, compile.Import{
			Module: identifier.GetCanonicalLabel().String(),
			Name:   identifier.GetStarlarkIdentifier().String(),
		})
	}
	for _, cell := range cells {
		if cell >= uint32(len(funcode.Locals)) {
			return nil, fmt.Errorf("cell %d refers to a local variable that does not exist", cell)
		}
		funcode.Cells = append(funcode.Cells, int(cell))
	}
	if m.GetNumParams() > uint32(len(funcode.Locals)) || m.GetMaxStack() > math.MaxInt32 {
		return nil, errors.New("function has invalid properties")
	}
	for i, nestedFunction := range functions {
		nestedFunctionFuncode := nestedFunction.GetFuncode()
		if nestedFunctionFuncode == nil {
			return nil, fmt.Errorf("nested function %d has no code", i)
		}
		nestedFuncode, err := DecodeFuncode(nestedFunctionFuncode, nestedFunction.GetName(), line+int32(nestedFunction.GetRelativeLine()))
		if err != nil {
			return nil, fmt.Errorf("nested function %d: %w", i, err)
		}
		funcode.Functions = append(funcode.Functions, nestedFuncode)
	}
	return funcode, nil
}

// storedFunction is a Starlark function that is backed by storage. Its
// code, and the values of its default parameters and free variables
// are loaded when the function is called.
type storedFunction[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	message model_core.Message[*model_starlark_pb.Function, TReference]

	funcode  *compile.Funcode
	defaults []eval.Value
	freeVars []*eval.Cell
}

var _ eval.Function = (*storedFunction[object.LocalReference, model_core.NoopReferenceMetadata])(nil)

// DecodeFunction decodes a Function message to a Starlark function.
// The function's code is only loaded from storage when it is called.
func DecodeFunction[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](message model_core.Message[*model_starlark_pb.Function, TReference]) eval.Function {
	return &storedFunction[TReference, TMetadata]{
		message: message,
	}
}

func (*storedFunction[TReference, TMetadata]) Type() string { return "function" }

func (*storedFunction[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) { return true, nil }

// GetFunctionDisplayName returns the name of a function that should be
// displayed to users.
func GetFunctionDisplayName(function *model_starlark_pb.Function) string {
	if namePath := function.GetNamePath(); len(namePath) > 0 {
		if name := namePath[len(namePath)-1].GetName(); name != "" {
			return name
		}
	}
	return compile.LambdaName
}

func (f *storedFunction[TReference, TMetadata]) Name() string {
	return GetFunctionDisplayName(f.message.Message)
}

// NamePath returns the path of the function within its file.
func (f *storedFunction[TReference, TMetadata]) NamePath() []compile.FunctionName {
	encodedNamePath := f.message.Message.GetNamePath()
	namePath := make([]compile.FunctionName, 0, len(encodedNamePath))
	for _, name := range encodedNamePath {
		namePath = append(namePath, decodeFunctionName(name))
	}
	return namePath
}

func (f *storedFunction[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<function " + f.Name() + ">")
	return nil
}

func (f *storedFunction[TReference, TMetadata]) CallInternal(thread *eval.Thread, args eval.Args) (eval.Value, error) {
	return eval.Call(thread, f, args)
}

func (f *storedFunction[TReference, TMetadata]) Funcode(thread *eval.Thread) (*compile.Funcode, error) {
	if f.funcode == nil {
		namePath := f.NamePath()
		if len(namePath) == 0 {
			return nil, errors.New("function has no name path")
		}
		filename, err := pg_label.NewCanonicalLabel(f.message.Message.GetFilename())
		if err != nil {
			return nil, fmt.Errorf("invalid filename %#v: %w", f.message.Message.GetFilename(), err)
		}
		funcode, err := getTopLevelFuncode[TReference](thread, filename, namePath[0])
		if err != nil {
			return nil, fmt.Errorf("function %#v: %w", f.Name(), err)
		}
		for _, name := range namePath[1:] {
			if funcode = funcode.FindNestedFunction(name); funcode == nil {
				return nil, fmt.Errorf("function %#v does not exist in file %#v", f.Name(), filename.String())
			}
		}
		f.funcode = funcode
	}
	return f.funcode, nil
}

func (f *storedFunction[TReference, TMetadata]) Module(*eval.Thread) (eval.Module, error) {
	filename, err := pg_label.NewCanonicalLabel(f.message.Message.GetFilename())
	if err != nil {
		return nil, fmt.Errorf("invalid filename %#v: %w", f.message.Message.GetFilename(), err)
	}
	return storedModule[TReference, TMetadata]{filename: filename}, nil
}

func (f *storedFunction[TReference, TMetadata]) Defaults(thread *eval.Thread) ([]eval.Value, error) {
	if f.defaults == nil {
		encodedDefaults := f.message.Message.GetClosure().GetDefaultParameters()
		defaults := make([]eval.Value, 0, len(encodedDefaults))
		for index, defaultParameter := range encodedDefaults {
			encodedValue := defaultParameter.GetValue()
			if encodedValue == nil {
				defaults = append(defaults, eval.Mandatory)
			} else {
				value, err := DecodeValue[TReference, TMetadata](thread, model_core.Nested(f.message, encodedValue), nil)
				if err != nil {
					return nil, fmt.Errorf("default parameter %d: %w", index, err)
				}
				defaults = append(defaults, value)
			}
		}
		f.defaults = defaults
	}
	return f.defaults, nil
}

func (f *storedFunction[TReference, TMetadata]) FreeVars(thread *eval.Thread) ([]*eval.Cell, error) {
	if f.freeVars == nil {
		encodedFreeVars := f.message.Message.GetClosure().GetFreeVariables()
		freeVars := make([]*eval.Cell, 0, len(encodedFreeVars))
		for index, freeVar := range encodedFreeVars {
			value, err := DecodeValue[TReference, TMetadata](thread, model_core.Nested(f.message, freeVar), nil)
			if err != nil {
				return nil, fmt.Errorf("free variable %d: %w", index, err)
			}
			freeVars = append(freeVars, eval.NewCell(value))
		}
		f.freeVars = freeVars
	}
	return f.freeVars, nil
}

func (f *storedFunction[TReference, TMetadata]) encodeFunction(options *ValueEncodingOptions[TReference, TMetadata]) model_core.PatchedMessage[*model_starlark_pb.Function, TMetadata] {
	return model_core.Patch(options.ObjectCapturer, f.message)
}

// GlobalResolver is called to obtain the value of a global variable
// that is declared in a .bzl file. In the context of an evaluation,
// this is typically implemented by requesting the CompiledBzlFileGlobal
// key.
type GlobalResolver[TReference any] = func(identifier pg_label.CanonicalStarlarkIdentifier) (model_core.Message[*model_starlark_pb.Value, TReference], error)

// GlobalResolverKey is the key under which an instance of
// GlobalResolver is stored in the thread local variables of a Starlark
// thread.
const GlobalResolverKey = "global_resolver"

// globalCacheKey is the key of a thread local variable that holds the
// values of globals that have been obtained through GlobalResolver.
// The values are only cached for the lifetime of the thread, as values
// that are obtained through GlobalResolver may not be shared between
// evaluations.
type globalCacheKey struct{}

// ResolveGlobal returns the value of a global variable that is declared
// in a .bzl file, by calling into the GlobalResolver that is stored in
// the thread. Values are cached on the thread.
func ResolveGlobal[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, identifier pg_label.CanonicalStarlarkIdentifier) (eval.Value, error) {
	cache, _ := thread.Local(globalCacheKey{}).(map[pg_label.CanonicalStarlarkIdentifier]eval.Value)
	if cache == nil {
		cache = map[pg_label.CanonicalStarlarkIdentifier]eval.Value{}
		thread.SetLocal(globalCacheKey{}, cache)
	}
	if value, ok := cache[identifier]; ok {
		return value, nil
	}

	globalResolver, ok := thread.Local(GlobalResolverKey).(GlobalResolver[TReference])
	if !ok {
		return nil, fmt.Errorf("global %#v cannot be resolved from within this context", identifier.String())
	}
	encodedValue, err := globalResolver(identifier)
	if err != nil {
		return nil, err
	}
	value, err := DecodeValue[TReference, TMetadata](thread, encodedValue, &identifier)
	if err != nil {
		return nil, fmt.Errorf("global %#v: %w", identifier.String(), err)
	}
	cache[identifier] = value
	return value, nil
}

// storedModule is the module of a function that is backed by storage.
// Globals and loaded symbols are resolved through the GlobalResolver,
// while predeclared names are obtained from the builtins of .bzl files.
type storedModule[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	filename pg_label.CanonicalLabel
}

func (m storedModule[TReference, TMetadata]) Filename() string {
	return m.filename.String()
}

func (m storedModule[TReference, TMetadata]) Global(thread *eval.Thread, name string) (eval.Value, error) {
	identifier, err := pg_label.NewStarlarkIdentifier(name)
	if err != nil {
		return nil, err
	}
	return ResolveGlobal[TReference, TMetadata](thread, m.filename.AppendStarlarkIdentifier(identifier))
}

func (storedModule[TReference, TMetadata]) Import(thread *eval.Thread, imp compile.Import) (eval.Value, error) {
	filename, err := pg_label.NewCanonicalLabel(imp.Module)
	if err != nil {
		return nil, fmt.Errorf("invalid module %#v: %w", imp.Module, err)
	}
	identifier, err := pg_label.NewStarlarkIdentifier(imp.Name)
	if err != nil {
		return nil, err
	}
	return ResolveGlobal[TReference, TMetadata](thread, filename.AppendStarlarkIdentifier(identifier))
}

func (storedModule[TReference, TMetadata]) Predeclared(thread *eval.Thread, name string) (eval.Value, error) {
	options, err := getValueDecodingOptions[TReference](thread)
	if err != nil {
		return nil, err
	}
	if options.PredeclaredResolver != nil {
		return options.PredeclaredResolver(thread, name)
	}
	if value, ok := options.BzlFileBuiltins[name]; ok {
		return value, nil
	}
	return nil, fmt.Errorf("predeclared name %s is not available", name)
}

// CanonicalizeImports rewrites the modules of all symbols that are
// loaded by a program to canonical labels. This needs to be called
// before a program is executed, so that the code of functions declared
// by the program can be written to storage.
func CanonicalizeImports(program *compile.Program, resolve func(module string) (pg_label.CanonicalLabel, error)) error {
	resolved := map[string]string{}
	var walk func(funcode *compile.Funcode) error
	walk = func(funcode *compile.Funcode) error {
		for i, imp := range funcode.Imports {
			canonicalModule, ok := resolved[imp.Module]
			if !ok {
				label, err := resolve(imp.Module)
				if err != nil {
					return fmt.Errorf("module %#v: %w", imp.Module, err)
				}
				canonicalModule = label.String()
				resolved[imp.Module] = canonicalModule
			}
			funcode.Imports[i].Module = canonicalModule
		}
		for _, nestedFuncode := range funcode.Functions {
			if err := walk(nestedFuncode); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(program.Toplevel)
}
