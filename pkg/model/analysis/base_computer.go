package analysis

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"bonanza.build/pkg/label"
	"bonanza.build/pkg/model/chunking"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	"bonanza.build/pkg/model/evaluation"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/remoteexecution"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/pool"

	"golang.org/x/sync/semaphore"
)

type baseComputerFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	filePool             pool.FilePool
	executionClient      remoteexecution.Client[*model_executewithstorage.Action[object.GlobalReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]]
	bzlFileBuiltins      map[string]eval.Value
	buildFileBuiltins    map[string]eval.Value
	objectStoreSemaphore *semaphore.Weighted
	binaryEncoderFactory model_encoding.BinaryEncoderFactory
}

// NewBaseComputerFactory creates a factory that can be provided
// evaluation.Executor to obtain computers capable of performing builds
// of Bazel projects.
func NewBaseComputerFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	filePool pool.FilePool,
	executionClient remoteexecution.Client[*model_executewithstorage.Action[object.GlobalReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]],
	bzlFileBuiltins map[string]eval.Value,
	buildFileBuiltins map[string]eval.Value,
	objectStoreSemaphore *semaphore.Weighted,
	binaryEncoderFactory model_encoding.BinaryEncoderFactory,
) evaluation.ComputerFactory[TReference, TMetadata] {
	return &baseComputerFactory[TReference, TMetadata]{
		filePool:             filePool,
		executionClient:      executionClient,
		bzlFileBuiltins:      bzlFileBuiltins,
		buildFileBuiltins:    buildFileBuiltins,
		objectStoreSemaphore: objectStoreSemaphore,
		binaryEncoderFactory: binaryEncoderFactory,
	}
}

func (cf *baseComputerFactory[TReference, TMetadata]) NewComputer(namespace object.Namespace, parsedObjectPoolIngester *model_parser.ParsedObjectPoolIngester[TReference], objectExporter model_core.ObjectExporter[TReference, object.LocalReference], chunkingParameters chunking.Parameters) evaluation.Computer[TReference, TMetadata] {
	return NewTypedComputer(
		NewBaseComputer[TReference, TMetadata](
			parsedObjectPoolIngester,
			namespace.ReferenceFormat,
			chunkingParameters,
			cf.filePool,
			model_executewithstorage.NewObjectExportingClient(
				model_executewithstorage.NewNamespaceAddingClient(
					cf.executionClient,
					namespace.InstanceName,
				),
				objectExporter,
			),
			cf.bzlFileBuiltins,
			cf.buildFileBuiltins,
			cf.objectStoreSemaphore,
			cf.binaryEncoderFactory,
		),
	)
}

type baseComputer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	parsedObjectPoolIngester *model_parser.ParsedObjectPoolIngester[TReference]
	referenceFormat          object.ReferenceFormat
	chunkingParameters       chunking.Parameters
	filePool                 pool.FilePool
	executionClient          remoteexecution.Client[*model_executewithstorage.Action[TReference], model_core.Decodable[TReference], model_core.Decodable[TReference]]
	bzlFileBuiltins          map[string]eval.Value
	buildFileBuiltins        map[string]eval.Value
	discardingObjectCapturer model_core.ObjectCapturer[TReference, model_core.NoopReferenceMetadata]
	objectStoreSemaphore     *semaphore.Weighted
	binaryEncoderFactory     model_encoding.BinaryEncoderFactory
}

// NewBaseComputer constructs a computer that is capable of performing
// builds of Bazel projects.
func NewBaseComputer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	parsedObjectPoolIngester *model_parser.ParsedObjectPoolIngester[TReference],
	referenceFormat object.ReferenceFormat,
	chunkingParameters chunking.Parameters,
	filePool pool.FilePool,
	executionClient remoteexecution.Client[*model_executewithstorage.Action[TReference], model_core.Decodable[TReference], model_core.Decodable[TReference]],
	bzlFileBuiltins map[string]eval.Value,
	buildFileBuiltins map[string]eval.Value,
	objectStoreSemaphore *semaphore.Weighted,
	binaryEncoderFactory model_encoding.BinaryEncoderFactory,
) Computer[TReference, TMetadata] {
	return &baseComputer[TReference, TMetadata]{
		parsedObjectPoolIngester: parsedObjectPoolIngester,
		referenceFormat:          referenceFormat,
		chunkingParameters:       chunkingParameters,
		filePool:                 filePool,
		executionClient:          executionClient,
		bzlFileBuiltins:          bzlFileBuiltins,
		buildFileBuiltins:        buildFileBuiltins,
		discardingObjectCapturer: model_core.NewDiscardingObjectCapturer[TReference](),
		objectStoreSemaphore:     objectStoreSemaphore,
		binaryEncoderFactory:     binaryEncoderFactory,
	}
}

// maximumLargeStringSizeBytes limits the amount of memory that may be
// used to hold strings that are too large to be stored inline. It is
// both the maximum number of bytes of such strings that a single
// Starlark thread may load into memory, and the maximum size of strings
// that are read from Value messages by code that inspects them
// directly.
const maximumLargeStringSizeBytes = 64 << 20

// getStringValue returns the contents of a Value message containing a
// string. If the message does not contain a string, ok is false.
func getStringValue[TReference object.BasicReference](ctx context.Context, fileReader *model_filesystem.FileReader[TReference], value model_core.Message[*model_starlark_pb.Value, TReference]) (s string, ok bool, err error) {
	return model_starlark.GetString(ctx, fileReader, value, maximumLargeStringSizeBytes)
}

// valueEncodingEnvironment contains the methods of an Environment
// that are needed to encode Starlark values.
type valueEncodingEnvironment[TReference, TMetadata any] interface {
	model_core.ObjectCapturer[TReference, TMetadata]
	FileCreationParametersObjectGetter
}

// valueEncodingOptionsFactory can be used to create instances of
// ValueEncodingOptions. It holds all dependencies that need to be
// obtained from the environment, so that the options can be created
// without needing to deal with missing dependencies.
type valueEncodingOptionsFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	computer               *baseComputer[TReference, TMetadata]
	objectCapturer         model_core.ObjectCapturer[TReference, TMetadata]
	fileCreationParameters *model_filesystem.FileCreationParameters
	objectEncoder          model_encoding.DeterministicBinaryEncoder
}

// newValueEncodingOptionsFactory creates a valueEncodingOptionsFactory,
// obtaining all of its dependencies from the environment.
func (c *baseComputer[TReference, TMetadata]) newValueEncodingOptionsFactory(e valueEncodingEnvironment[TReference, TMetadata], starlarkValueEncoders *StarlarkValueEncoders[TReference]) (*valueEncodingOptionsFactory[TReference, TMetadata], error) {
	// Strings that are too large to be stored inline are stored
	// using the same parameters as files, so that they can share
	// data with files.
	fileCreationParameters, ok := e.GetFileCreationParametersObject(&model_analysis_pb.FileCreationParameters_Key{})
	if !ok {
		return nil, evaluation.ErrMissingDependency
	}
	return &valueEncodingOptionsFactory[TReference, TMetadata]{
		computer:               c,
		objectCapturer:         e,
		fileCreationParameters: fileCreationParameters,
		objectEncoder:          starlarkValueEncoders.Encoder,
	}, nil
}

// newValueEncodingOptions creates ValueEncodingOptions that can be
// used to encode Starlark values.
func (f *valueEncodingOptionsFactory[TReference, TMetadata]) newValueEncodingOptions(ctx context.Context, currentFilename *label.CanonicalLabel) *model_starlark.ValueEncodingOptions[TReference, TMetadata] {
	return &model_starlark.ValueEncodingOptions[TReference, TMetadata]{
		CurrentFilename:          currentFilename,
		Context:                  ctx,
		ObjectEncoder:            f.objectEncoder,
		ObjectReferenceFormat:    f.computer.referenceFormat,
		ObjectCapturer:           f.objectCapturer,
		ObjectChunkingParameters: f.computer.chunkingParameters,
		FileCreationParameters:   f.fileCreationParameters,
	}
}

type loadBzlGlobalsEnvironment[TReference any] interface {
	labelResolverEnvironment[TReference]

	BuiltinsModuleNamesValueGetter[TReference]
	CompiledBzlFileValueGetter[TReference]
}

// getBzlGlobal returns the value of a global variable that is declared
// in a .bzl file, which is compiled using a given set of builtins
// modules.
func getBzlGlobal[TReference object.BasicReference](ctx context.Context, e CompiledBzlFileValueGetter[TReference], valueReaders *model_starlark.ValueReaders[TReference], identifier label.CanonicalStarlarkIdentifier, builtinsModuleNames []string) (model_core.Message[*model_starlark_pb.Value, TReference], error) {
	compiledBzlFile := e.GetCompiledBzlFileValue(&model_analysis_pb.CompiledBzlFile_Key{
		Label:               identifier.GetCanonicalLabel().String(),
		BuiltinsModuleNames: builtinsModuleNames,
	})
	if !compiledBzlFile.IsSet() {
		return model_core.Message[*model_starlark_pb.Value, TReference]{}, evaluation.ErrMissingDependency
	}
	globals := model_core.Nested(compiledBzlFile, compiledBzlFile.Message.CompiledProgram.GetGlobals())
	if _, ok := slices.BinarySearch(globals.Message.GetKeys(), identifier.GetStarlarkIdentifier().String()); !ok {
		return model_core.Message[*model_starlark_pb.Value, TReference]{}, fmt.Errorf("file %#v does not contain global %#v", identifier.GetCanonicalLabel().String(), identifier.GetStarlarkIdentifier().String())
	}
	return model_starlark.GetStructFieldValue(ctx, valueReaders.List, globals, identifier.GetStarlarkIdentifier().String())
}

// canonicalizeLoadLabel converts the label provided to a load()
// statement to a canonical label.
func canonicalizeLoadLabel[TReference any](e labelResolverEnvironment[TReference], canonicalPackage label.CanonicalPackage, loadLabelStr string) (label.CanonicalLabel, error) {
	apparentLoadLabel, err := canonicalPackage.AppendLabel(loadLabelStr)
	if err != nil {
		return label.CanonicalLabel{}, fmt.Errorf("invalid label %#v in load() statement: %w", loadLabelStr, err)
	}
	canonicalLoadLabel, err := label.Canonicalize(newLabelResolver(e), canonicalPackage.GetCanonicalRepo(), apparentLoadLabel)
	if err != nil {
		return label.CanonicalLabel{}, fmt.Errorf("failed to resolve label %#v in load() statement: %w", apparentLoadLabel.String(), err)
	}
	return canonicalLoadLabel, nil
}

// loadProgram parses, resolves and compiles a .bzl or BUILD file. The
// labels in its load() statements are converted to canonical labels,
// and it is validated that the symbols that are loaded exist. This
// mirrors Bazel, which reports missing symbols before executing any
// code.
func loadProgram[TReference object.BasicReference](e loadBzlGlobalsEnvironment[TReference], filename label.CanonicalLabel, contents []byte, dialect *resolve.Dialect, predeclared map[string]eval.Value, builtinsModuleNames []string) (*compile.Program, error) {
	f, err := syntax.Parse(filename.String(), contents, syntax.Options{})
	if err != nil {
		return nil, err
	}
	universe := eval.Universe()
	m, err := resolve.File(
		f,
		dialect,
		func(name string) bool {
			_, ok := predeclared[name]
			return ok
		},
		func(name string) bool {
			_, ok := universe[name]
			return ok
		},
	)
	if err != nil {
		return nil, err
	}
	program := compile.File(f, m)

	canonicalPackage := filename.GetCanonicalPackage()
	if err := model_starlark.CanonicalizeImports(program, func(module string) (label.CanonicalLabel, error) {
		return canonicalizeLoadLabel(e, canonicalPackage, module)
	}); err != nil {
		return nil, err
	}

	// Check that all loaded symbols exist. Request all files
	// before returning, so that they are computed in parallel.
	missingDependencies := false
	for _, loadedSymbol := range m.Imports {
		canonicalLoadLabel, err := canonicalizeLoadLabel(e, canonicalPackage, loadedSymbol.Module)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
			return nil, err
		}
		compiledBzlFile := e.GetCompiledBzlFileValue(&model_analysis_pb.CompiledBzlFile_Key{
			Label:               canonicalLoadLabel.String(),
			BuiltinsModuleNames: builtinsModuleNames,
		})
		if !compiledBzlFile.IsSet() {
			missingDependencies = true
			continue
		}
		if _, ok := slices.BinarySearch(compiledBzlFile.Message.CompiledProgram.GetGlobals().GetKeys(), loadedSymbol.Name); !ok {
			return nil, fmt.Errorf("%s:%d:%d: file %#v does not contain symbol %#v", filename.String(), loadedSymbol.Binding.First.NamePos.Line, loadedSymbol.Binding.First.NamePos.Column, canonicalLoadLabel.String(), loadedSymbol.Name)
		}
	}
	if missingDependencies {
		return nil, evaluation.ErrMissingDependency
	}
	return program, nil
}

// newBzlImporter returns a function that can be provided to
// eval.NewInMemoryModule() to resolve the symbols that are loaded by a
// .bzl or BUILD file. Symbols are only decoded when they are used.
func newBzlImporter[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](ctx context.Context, e loadBzlGlobalsEnvironment[TReference], valueReaders *model_starlark.ValueReaders[TReference], builtinsModuleNames []string) func(thread *eval.Thread, imp compile.Import) (eval.Value, error) {
	return func(thread *eval.Thread, imp compile.Import) (eval.Value, error) {
		filename, err := label.NewCanonicalLabel(imp.Module)
		if err != nil {
			return nil, err
		}
		name, err := label.NewStarlarkIdentifier(imp.Name)
		if err != nil {
			return nil, err
		}
		identifier := filename.AppendStarlarkIdentifier(name)
		value, err := getBzlGlobal(ctx, e, valueReaders, identifier, builtinsModuleNames)
		if err != nil {
			return nil, err
		}
		return model_starlark.DecodeValue[TReference, TMetadata](thread, value, &identifier)
	}
}

// formatStarlarkError converts errors returned by the Starlark
// interpreter to errors containing a backtrace.
func formatStarlarkError(err error) error {
	if !errors.Is(err, evaluation.ErrMissingDependency) {
		var evalErr *eval.Error
		if errors.As(err, &evalErr) {
			return errors.New(evalErr.Backtrace())
		}
	}
	return err
}

// trimBuiltinModuleNames truncates the list of built-in module names up
// to a provided module name. This needs to be called when attempting to
// load() files belonging to a built-in module, so that evaluating code
// belonging to the built-in module does not result into cycles.
func trimBuiltinModuleNames(builtinsModuleNames []string, module label.Module) []string {
	moduleStr := module.String()
	i := 0
	for i < len(builtinsModuleNames) && builtinsModuleNames[i] != moduleStr {
		i++
	}
	return builtinsModuleNames[:i]
}

// newStarlarkThread creates a Starlark thread that is capable of
// calling functions that are backed by storage. Globals and loaded
// symbols referenced by these functions are resolved through
// CompiledBzlFile, while predeclared names are resolved against the
// builtins of .bzl files, including the ones declared by builtins
// modules. These builtins are only computed when needed.
func (c *baseComputer[TReference, TMetadata]) newStarlarkThread(ctx context.Context, e loadBzlGlobalsEnvironment[TReference], starlarkValueEncoders *StarlarkValueEncoders[TReference], builtinsModuleNames []string) (*eval.Thread, error) {
	valueReaders := &starlarkValueEncoders.Readers

	thread := eval.NewThread(ctx, eval.Universe())
	thread.MaximumLargeStringConversionBytes = maximumLargeStringSizeBytes
	thread.SetLocal(model_starlark.LabelResolverKey, newLabelResolver(e))
	thread.SetLocal(model_starlark.GlobalResolverKey, newGlobalResolver(ctx, e, valueReaders, builtinsModuleNames))
	thread.SetLocal(model_starlark.CompiledProgramResolverKey, newCompiledProgramResolver(e, builtinsModuleNames))

	decodingOptions := starlarkValueEncoders.newValueDecodingOptions(func(resolvedLabel label.ResolvedLabel) (eval.Value, error) {
		return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
	})
	var bzlFileBuiltins map[string]eval.Value
	decodingOptions.PredeclaredResolver = func(thread *eval.Thread, name string) (eval.Value, error) {
		if bzlFileBuiltins == nil {
			var err error
			bzlFileBuiltins, err = c.getBzlFileBuiltins(thread, e, valueReaders, builtinsModuleNames)
			if err != nil {
				return nil, err
			}
		}
		if value, ok := bzlFileBuiltins[name]; ok {
			return value, nil
		}
		return nil, fmt.Errorf("predeclared name %s is not available", name)
	}
	thread.SetLocal(model_starlark.ValueDecodingOptionsKey, decodingOptions)
	return thread, nil
}

// ExecutionClientForTesting is used to generate mocks for unit testing
// BaseComputer.
type ExecutionClientForTesting remoteexecution.Client[*model_executewithstorage.Action[model_core.CreatedObjectTree], model_core.Decodable[model_core.CreatedObjectTree], model_core.Decodable[model_core.CreatedObjectTree]]
