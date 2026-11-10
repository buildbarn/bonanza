package analysis

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	"bonanza.build/pkg/model/evaluation"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/remoteexecution"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/pool"
	"github.com/buildbarn/bb-storage/pkg/util"

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

func (cf *baseComputerFactory[TReference, TMetadata]) NewComputer(namespace object.Namespace, parsedObjectPoolIngester *model_parser.ParsedObjectPoolIngester[TReference], objectExporter model_core.ObjectExporter[TReference, object.LocalReference]) evaluation.Computer[TReference, TMetadata] {
	return NewTypedComputer(
		NewBaseComputer[TReference, TMetadata](
			parsedObjectPoolIngester,
			namespace.ReferenceFormat,
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
	filePool                 pool.FilePool
	executionClient          remoteexecution.Client[*model_executewithstorage.Action[TReference], model_core.Decodable[TReference], model_core.Decodable[TReference]]
	bzlFileBuiltins          map[string]eval.Value
	buildFileBuiltins        map[string]eval.Value
	discardingObjectCapturer model_core.ObjectCapturer[TReference, model_core.NoopReferenceMetadata]
	objectStoreSemaphore     *semaphore.Weighted
	binaryEncoderFactory     model_encoding.BinaryEncoderFactory

	// Readers for various message types.
	// TODO: These should likely be removed and instantiated later
	// on, so that we can encrypt all data in storage.
	valueReaders                        model_starlark.ValueReaders[TReference]
	argsReader                          model_parser.MessageObjectReader[TReference, []*model_analysis_pb.Args]
	argsAddReader                       model_parser.MessageObjectReader[TReference, []*model_analysis_pb.Args_Leaf_Add]
	buildSettingOverrideReader          model_parser.MessageObjectReader[TReference, []*model_analysis_pb.BuildSettingOverride]
	commandOutputsReader                model_parser.MessageObjectReader[TReference, *model_command_pb.Outputs]
	configuredTargetActionReader        model_parser.MessageObjectReader[TReference, []*model_analysis_pb.ConfiguredTarget_Value_Action]
	configuredTargetOutputReader        model_parser.MessageObjectReader[TReference, []*model_analysis_pb.ConfiguredTarget_Value_Output]
	filesToRunProviderReader            model_parser.MessageObjectReader[TReference, []*model_analysis_pb.FilesToRunProvider]
	macroExpansionContextEntryReader    model_parser.MessageObjectReader[TReference, []*model_starlark_pb.MacroExpansionContext_Entry]
	macroInstanceEntryReader            model_parser.MessageObjectReader[TReference, []*model_starlark_pb.MacroInstance_Entry]
	moduleExtensionReposValueRepoReader model_parser.MessageObjectReader[TReference, []*model_analysis_pb.ModuleExtensionRepos_Value_Repo]
	packageGroupOverridesReader         model_parser.MessageObjectReader[TReference, *model_starlark_pb.PackageGroup_Subpackages_Overrides]
	packageValueTargetReader            model_parser.MessageObjectReader[TReference, []*model_analysis_pb.Package_Value_Target]
	targetLabelReader                   model_parser.MessageObjectReader[TReference, []*model_analysis_pb.TargetLabel]
}

// NewBaseComputer constructs a computer that is capable of performing
// builds of Bazel projects.
func NewBaseComputer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	parsedObjectPoolIngester *model_parser.ParsedObjectPoolIngester[TReference],
	referenceFormat object.ReferenceFormat,
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
		filePool:                 filePool,
		executionClient:          executionClient,
		bzlFileBuiltins:          bzlFileBuiltins,
		buildFileBuiltins:        buildFileBuiltins,
		discardingObjectCapturer: model_core.NewDiscardingObjectCapturer[TReference](),
		objectStoreSemaphore:     objectStoreSemaphore,
		binaryEncoderFactory:     binaryEncoderFactory,

		// TODO: Set up encoding!
		valueReaders: model_starlark.ValueReaders[TReference]{
			CompiledProgramFunctions: model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.CompiledProgram_FunctionEntry](),
			),
			Dict: model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.Dict_Entry](),
			),
			List: model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.List_Element](),
			),
			PackageGroupOverrides: model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewProtoObjectParser[TReference, model_starlark_pb.PackageGroup_Subpackages_Overrides](),
			),
			TestSuiteTest: model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.TestSuite_Test](),
			),
		},
		argsReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.Args](),
		),
		argsAddReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.Args_Leaf_Add](),
		),
		buildSettingOverrideReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.BuildSettingOverride](),
		),
		configuredTargetOutputReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.ConfiguredTarget_Value_Output](),
		),
		commandOutputsReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoObjectParser[TReference, model_command_pb.Outputs](),
		),
		configuredTargetActionReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.ConfiguredTarget_Value_Action](),
		),
		filesToRunProviderReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.FilesToRunProvider](),
		),
		macroExpansionContextEntryReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.MacroExpansionContext_Entry](),
		),
		macroInstanceEntryReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.MacroInstance_Entry](),
		),
		moduleExtensionReposValueRepoReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.ModuleExtensionRepos_Value_Repo](),
		),
		packageGroupOverridesReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoObjectParser[TReference, model_starlark_pb.PackageGroup_Subpackages_Overrides](),
		),
		packageValueTargetReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.Package_Value_Target](),
		),
		targetLabelReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.TargetLabel](),
		),
	}
}

func (baseComputer[TReference, TMetadata]) getValueObjectEncoder() model_encoding.DeterministicBinaryEncoder {
	// TODO: Use a proper encoder!
	return model_encoding.NewChainedDeterministicBinaryEncoder(nil)
}

// maximumLargeStringSizeBytes limits the amount of memory that may be
// used to hold strings that are too large to be stored inline. It is
// both the maximum number of bytes of such strings that a single
// Starlark thread may load into memory, and the maximum size of strings
// that are read from Value messages by code that inspects them
// directly.
const maximumLargeStringSizeBytes = 64 << 20

// getFileReader returns the FileReader that can be used to read the
// contents of files, and strings that are too large to be stored
// inline.
func getFileReader[TReference object.BasicReference](e FileAccessParametersObjectGetter[TReference]) (*model_filesystem.FileReader[TReference], error) {
	fileReader, ok := e.GetFileAccessParametersObject(&model_analysis_pb.FileAccessParameters_Key{})
	if !ok {
		return nil, evaluation.ErrMissingDependency
	}
	return fileReader, nil
}

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
}

// newValueEncodingOptionsFactory creates a valueEncodingOptionsFactory,
// obtaining all of its dependencies from the environment.
func (c *baseComputer[TReference, TMetadata]) newValueEncodingOptionsFactory(e valueEncodingEnvironment[TReference, TMetadata]) (*valueEncodingOptionsFactory[TReference, TMetadata], error) {
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
	}, nil
}

// newValueEncodingOptions creates ValueEncodingOptions that can be
// used to encode Starlark values.
func (f *valueEncodingOptionsFactory[TReference, TMetadata]) newValueEncodingOptions(ctx context.Context, currentFilename *label.CanonicalLabel) *model_starlark.ValueEncodingOptions[TReference, TMetadata] {
	return &model_starlark.ValueEncodingOptions[TReference, TMetadata]{
		CurrentFilename:        currentFilename,
		Context:                ctx,
		ObjectEncoder:          f.computer.getValueObjectEncoder(),
		ObjectReferenceFormat:  f.computer.referenceFormat,
		ObjectCapturer:         f.objectCapturer,
		ObjectMinimumSizeBytes: 32 * 1024,
		ObjectHorizonSizeBytes: 8 * 32 * 1024,
		FileCreationParameters: f.fileCreationParameters,
	}
}

// valueDecodingOptionsFactory can be used to create instances of
// ValueDecodingOptions, whose dependencies have already been obtained
// from the environment.
type valueDecodingOptionsFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	computer *baseComputer[TReference, TMetadata]
	readers  model_starlark.ValueReaders[TReference]
}

// newValueDecodingOptionsFactory creates a valueDecodingOptionsFactory,
// obtaining all of its dependencies from the environment.
func (c *baseComputer[TReference, TMetadata]) newValueDecodingOptionsFactory(e FileAccessParametersObjectGetter[TReference]) (*valueDecodingOptionsFactory[TReference, TMetadata], error) {
	fileReader, err := getFileReader(e)
	if err != nil {
		return nil, err
	}
	// The file reader depends on parameters obtained from the
	// environment, meaning it cannot be set on the readers that are
	// shared by all evaluations.
	readers := c.valueReaders
	readers.FileReader = fileReader
	return &valueDecodingOptionsFactory[TReference, TMetadata]{
		computer: c,
		readers:  readers,
	}, nil
}

// newValueDecodingOptions creates ValueDecodingOptions that can be used
// to decode Starlark values.
func (f *valueDecodingOptionsFactory[TReference, TMetadata]) newValueDecodingOptions(labelCreator func(label.ResolvedLabel) (eval.Value, error)) *model_starlark.ValueDecodingOptions[TReference] {
	return &model_starlark.ValueDecodingOptions[TReference]{
		Readers:         &f.readers,
		LabelCreator:    labelCreator,
		BzlFileBuiltins: f.computer.bzlFileBuiltins,
	}
}

func (c *baseComputer[TReference, TMetadata]) getInlinedTreeOptions() *inlinedtree.Options {
	return &inlinedtree.Options{
		ReferenceFormat:  c.referenceFormat,
		MaximumSizeBytes: 32 * 1024,
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
func (c *baseComputer[TReference, TMetadata]) getBzlGlobal(ctx context.Context, e CompiledBzlFileValueGetter[TReference], identifier label.CanonicalStarlarkIdentifier, builtinsModuleNames []string) (model_core.Message[*model_starlark_pb.Value, TReference], error) {
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
	return model_starlark.GetStructFieldValue(ctx, c.valueReaders.List, globals, identifier.GetStarlarkIdentifier().String())
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
func (*baseComputer[TReference, TMetadata]) loadProgram(e loadBzlGlobalsEnvironment[TReference], filename label.CanonicalLabel, contents []byte, dialect *resolve.Dialect, predeclared map[string]eval.Value, builtinsModuleNames []string) (*compile.Program, error) {
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
func (c *baseComputer[TReference, TMetadata]) newBzlImporter(ctx context.Context, e loadBzlGlobalsEnvironment[TReference], builtinsModuleNames []string) func(thread *eval.Thread, imp compile.Import) (eval.Value, error) {
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
		value, err := c.getBzlGlobal(ctx, e, identifier, builtinsModuleNames)
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

type starlarkThreadEnvironment[TReference object.BasicReference] interface {
	loadBzlGlobalsEnvironment[TReference]
	FileAccessParametersObjectGetter[TReference]
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
func (c *baseComputer[TReference, TMetadata]) newStarlarkThread(ctx context.Context, e starlarkThreadEnvironment[TReference], builtinsModuleNames []string) (*eval.Thread, error) {
	decodingOptionsFactory, err := c.newValueDecodingOptionsFactory(e)
	if err != nil {
		return nil, err
	}

	thread := eval.NewThread(ctx, eval.Universe())
	thread.MaximumLargeStringConversionBytes = maximumLargeStringSizeBytes
	thread.SetLocal(model_starlark.LabelResolverKey, newLabelResolver(e))
	thread.SetLocal(model_starlark.GlobalResolverKey, c.newGlobalResolver(ctx, e, builtinsModuleNames))
	thread.SetLocal(model_starlark.CompiledProgramResolverKey, c.newCompiledProgramResolver(e, builtinsModuleNames))

	decodingOptions := decodingOptionsFactory.newValueDecodingOptions(func(resolvedLabel label.ResolvedLabel) (eval.Value, error) {
		return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
	})
	var bzlFileBuiltins map[string]eval.Value
	decodingOptions.PredeclaredResolver = func(thread *eval.Thread, name string) (eval.Value, error) {
		if bzlFileBuiltins == nil {
			var err error
			bzlFileBuiltins, err = c.getBzlFileBuiltins(thread, e, builtinsModuleNames)
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

func (c *baseComputer[TReference, TMetadata]) ComputeBuildResultValue(ctx context.Context, key *model_analysis_pb.BuildResult_Key, e BuildResultEnvironment[TReference, TMetadata]) (PatchedBuildResultValue[TMetadata], error) {
	buildSpecificationMessage := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecificationMessage.IsSet() {
		return PatchedBuildResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	buildSpecification := buildSpecificationMessage.Message

	rootModuleName := buildSpecification.RootModuleName
	rootModule, err := label.NewModule(rootModuleName)
	if err != nil {
		return PatchedBuildResultValue[TMetadata]{}, fmt.Errorf("invalid root module name %#v: %w", rootModuleName, err)
	}
	rootRepo := rootModule.ToModuleInstance(nil).GetBareCanonicalRepo()
	rootPackage := rootRepo.GetRootPackage()

	thread, err := c.newStarlarkThread(ctx, e, buildSpecification.BuiltinsModuleNames)
	if err != nil {
		return PatchedBuildResultValue[TMetadata]{}, err
	}
	missingDependencies := false

	// Determine which targets to build. The query is only expanded
	// after creating the configurations, so that the dependencies
	// of both are requested together.
	canonicalQuery, err := parseRootModuleQuery(e, key.Query)
	gotCanonicalQuery := err == nil
	if !gotCanonicalQuery {
		if !errors.Is(err, evaluation.ErrMissingDependency) {
			return PatchedBuildResultValue[TMetadata]{}, err
		}
		missingDependencies = true
	}

	for i, configuration := range key.Configurations {
		targetPlatformConfigurationReference, err := c.createInitialConfiguration(ctx, e, thread, rootPackage, configuration)
		if err != nil {
			if !errors.Is(err, evaluation.ErrMissingDependency) {
				return PatchedBuildResultValue[TMetadata]{}, fmt.Errorf("failed to create initial configuration for configuration at index %d: %w", i, err)
			}
			missingDependencies = true
			continue
		}
		clonedConfigurationReference := model_core.Unpatch(
			e,
			targetPlatformConfigurationReference,
		).Decay()

		if !gotCanonicalQuery {
			continue
		}
		var iterErr error
		for canonicalTargetLabel := range c.allQueryLabels(
			ctx,
			e,
			canonicalQuery,
			&iterErr,
		) {
			visibleTargetValue := e.GetVisibleTargetValue(
				model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.VisibleTarget_Key {
					return &model_analysis_pb.VisibleTarget_Key{
						FromPackage:            canonicalTargetLabel.GetCanonicalPackage().String(),
						ToLabel:                canonicalTargetLabel.String(),
						ConfigurationReference: model_core.Patch(e, clonedConfigurationReference).Merge(patcher),
					}
				}),
			)
			if !visibleTargetValue.IsSet() {
				missingDependencies = true
				continue
			}

			targetCompletionValue := e.GetTargetCompletionValue(
				model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetCompletion_Key {
					return &model_analysis_pb.TargetCompletion_Key{
						Label:                  visibleTargetValue.Message.Label,
						ConfigurationReference: model_core.Patch(e, clonedConfigurationReference).Merge(patcher),
					}
				}),
			)
			if !targetCompletionValue.IsSet() {
				missingDependencies = true
			}
		}
		if iterErr != nil {
			if !errors.Is(iterErr, evaluation.ErrMissingDependency) {
				return PatchedBuildResultValue[TMetadata]{}, fmt.Errorf("failed to expand query: %w", iterErr)
			}
			missingDependencies = true
		}
	}
	if missingDependencies {
		return PatchedBuildResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.BuildResult_Value{}), nil
}

func (baseComputer[TReference, TMetadata]) ComputeBuiltinsModuleNamesValue(ctx context.Context, key *model_analysis_pb.BuiltinsModuleNames_Key, e BuiltinsModuleNamesEnvironment[TReference, TMetadata]) (PatchedBuiltinsModuleNamesValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedBuiltinsModuleNamesValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.BuiltinsModuleNames_Value{
		BuiltinsModuleNames: buildSpecification.Message.BuiltinsModuleNames,
	}), nil
}

func (baseComputer[TReference, TMetadata]) ComputeDirectoryAccessParametersValue(ctx context.Context, key *model_analysis_pb.DirectoryAccessParameters_Key, e DirectoryAccessParametersEnvironment[TReference, TMetadata]) (PatchedDirectoryAccessParametersValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedDirectoryAccessParametersValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.DirectoryAccessParameters_Value{
		DirectoryAccessParameters: buildSpecification.Message.DirectoryCreationParameters.GetAccess(),
	}), nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeRepoDefaultAttrsValue(ctx context.Context, key *model_analysis_pb.RepoDefaultAttrs_Key, e RepoDefaultAttrsEnvironment[TReference, TMetadata]) (PatchedRepoDefaultAttrsValue[TMetadata], error) {
	canonicalRepo, err := label.NewCanonicalRepo(key.CanonicalRepo)
	if err != nil {
		return PatchedRepoDefaultAttrsValue[TMetadata]{}, fmt.Errorf("invalid canonical repo: %w", err)
	}

	repoFileName := util.Must(label.NewTargetName("REPO.bazel"))
	repoFileProperties := e.GetFilePropertiesValue(&model_analysis_pb.FileProperties_Key{
		CanonicalRepo: canonicalRepo.String(),
		Path:          repoFileName.String(),
	})

	fileReader, gotFileReader := e.GetFileAccessParametersObject(&model_analysis_pb.FileAccessParameters_Key{})
	if !repoFileProperties.IsSet() || !gotFileReader {
		return PatchedRepoDefaultAttrsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Read the contents of REPO.bazel.
	repoFileLabel := canonicalRepo.GetRootPackage().AppendTargetName(repoFileName)
	if repoFileProperties.Message.Exists == nil {
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.RepoDefaultAttrs_Value{
			InheritableAttrs: &model_starlark.DefaultInheritableAttrs,
		}), nil
	}
	repoFileContentsEntry, err := model_filesystem.NewFileContentsEntryFromProto(
		model_core.Nested(repoFileProperties, repoFileProperties.Message.Exists.GetContents()),
	)
	if err != nil {
		return PatchedRepoDefaultAttrsValue[TMetadata]{}, fmt.Errorf("invalid contents for file %#v: %w", repoFileLabel.String(), err)
	}
	repoFileData, err := fileReader.FileReadAll(ctx, repoFileContentsEntry, 1<<20)
	if err != nil {
		return PatchedRepoDefaultAttrsValue[TMetadata]{}, err
	}

	// Extract the default inheritable attrs from REPO.bazel.
	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.RepoDefaultAttrs_Value, error) {
		defaultAttrs, err := model_starlark.ParseRepoDotBazel[TReference](
			ctx,
			string(repoFileData),
			canonicalRepo.GetRootPackage().AppendTargetName(repoFileName),
			c.getValueObjectEncoder(),
			c.getInlinedTreeOptions(),
			e,
			newLabelResolver(e),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %#v: %w", repoFileLabel.String(), err)
		}
		return &model_analysis_pb.RepoDefaultAttrs_Value{
			InheritableAttrs: defaultAttrs.Merge(patcher),
		}, nil
	})
}

// ExecutionClientForTesting is used to generate mocks for unit testing
// BaseComputer.
type ExecutionClientForTesting remoteexecution.Client[*model_executewithstorage.Action[model_core.CreatedObjectTree], model_core.Decodable[model_core.CreatedObjectTree], model_core.Decodable[model_core.CreatedObjectTree]]
