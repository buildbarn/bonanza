package analysis

import (
	"context"
	"errors"
	"fmt"

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
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/pool"
	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/semaphore"

	"go.starlark.net/starlark"
)

type baseComputerFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	filePool             pool.FilePool
	executionClient      remoteexecution.Client[*model_executewithstorage.Action[object.GlobalReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]]
	bzlFileBuiltins      starlark.StringDict
	buildFileBuiltins    starlark.StringDict
	objectStoreSemaphore *semaphore.Weighted
}

// NewBaseComputerFactory creates a factory that can be provided
// evaluation.Executor to obtain computers capable of performing builds
// of Bazel projects.
func NewBaseComputerFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	filePool pool.FilePool,
	executionClient remoteexecution.Client[*model_executewithstorage.Action[object.GlobalReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]],
	bzlFileBuiltins starlark.StringDict,
	buildFileBuiltins starlark.StringDict,
	objectStoreSemaphore *semaphore.Weighted,
) evaluation.ComputerFactory[TReference, TMetadata] {
	return &baseComputerFactory[TReference, TMetadata]{
		filePool:             filePool,
		executionClient:      executionClient,
		bzlFileBuiltins:      bzlFileBuiltins,
		buildFileBuiltins:    buildFileBuiltins,
		objectStoreSemaphore: objectStoreSemaphore,
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
		),
	)
}

type baseComputer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	parsedObjectPoolIngester *model_parser.ParsedObjectPoolIngester[TReference]
	referenceFormat          object.ReferenceFormat
	filePool                 pool.FilePool
	executionClient          remoteexecution.Client[*model_executewithstorage.Action[TReference], model_core.Decodable[TReference], model_core.Decodable[TReference]]
	bzlFileBuiltins          starlark.StringDict
	buildFileBuiltins        starlark.StringDict
	discardingObjectCapturer model_core.ObjectCapturer[TReference, model_core.NoopReferenceMetadata]
	objectStoreSemaphore     *semaphore.Weighted

	// Readers for various message types.
	// TODO: These should likely be removed and instantiated later
	// on, so that we can encrypt all data in storage.
	valueReaders                                 model_starlark.ValueReaders[TReference]
	argsReader                                   model_parser.MessageObjectReader[TReference, []*model_analysis_pb.Args]
	argsAddReader                                model_parser.MessageObjectReader[TReference, []*model_analysis_pb.Args_Leaf_Add]
	buildSettingOverrideReader                   model_parser.MessageObjectReader[TReference, []*model_analysis_pb.BuildSettingOverride]
	commandOutputsReader                         model_parser.MessageObjectReader[TReference, *model_command_pb.Outputs]
	configuredTargetActionReader                 model_parser.MessageObjectReader[TReference, []*model_analysis_pb.ConfiguredTarget_Value_Action]
	configuredTargetOutputReader                 model_parser.MessageObjectReader[TReference, []*model_analysis_pb.ConfiguredTarget_Value_Output]
	filesToRunProviderReader                     model_parser.MessageObjectReader[TReference, []*model_analysis_pb.FilesToRunProvider]
	moduleExtensionReposValueRepoReader          model_parser.MessageObjectReader[TReference, []*model_analysis_pb.ModuleExtensionRepos_Value_Repo]
	packageValueTargetReader                     model_parser.MessageObjectReader[TReference, []*model_analysis_pb.Package_Value_Target]
	targetPatternExpansionValueTargetLabelReader model_parser.MessageObjectReader[TReference, []*model_analysis_pb.TargetPatternExpansion_Value_TargetLabel]
}

// NewBaseComputer constructs a computer that is capable of performing
// builds of Bazel projects.
func NewBaseComputer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	parsedObjectPoolIngester *model_parser.ParsedObjectPoolIngester[TReference],
	referenceFormat object.ReferenceFormat,
	filePool pool.FilePool,
	executionClient remoteexecution.Client[*model_executewithstorage.Action[TReference], model_core.Decodable[TReference], model_core.Decodable[TReference]],
	bzlFileBuiltins starlark.StringDict,
	buildFileBuiltins starlark.StringDict,
	objectStoreSemaphore *semaphore.Weighted,
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

		// TODO: Set up encoding!
		valueReaders: model_starlark.ValueReaders[TReference]{
			Dict: model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.Dict_Entry](),
			),
			List: model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.List_Element](),
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
		moduleExtensionReposValueRepoReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.ModuleExtensionRepos_Value_Repo](),
		),
		packageValueTargetReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.Package_Value_Target](),
		),
		targetPatternExpansionValueTargetLabelReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.TargetPatternExpansion_Value_TargetLabel](),
		),
	}
}

func (baseComputer[TReference, TMetadata]) getValueObjectEncoder() model_encoding.DeterministicBinaryEncoder {
	// TODO: Use a proper encoder!
	return model_encoding.NewChainedDeterministicBinaryEncoder(nil)
}

func (c *baseComputer[TReference, TMetadata]) getValueEncodingOptions(ctx context.Context, objectCapturer model_core.ObjectCapturer[TReference, TMetadata], currentFilename *label.CanonicalLabel) *model_starlark.ValueEncodingOptions[TReference, TMetadata] {
	return &model_starlark.ValueEncodingOptions[TReference, TMetadata]{
		CurrentFilename:        currentFilename,
		Context:                ctx,
		ObjectEncoder:          c.getValueObjectEncoder(),
		ObjectReferenceFormat:  c.referenceFormat,
		ObjectCapturer:         objectCapturer,
		ObjectMinimumSizeBytes: 32 * 1024,
		ObjectMaximumSizeBytes: 128 * 1024,
	}
}

func (c *baseComputer[TReference, TMetadata]) getValueDecodingOptions(ctx context.Context, labelCreator func(label.ResolvedLabel) (starlark.Value, error)) *model_starlark.ValueDecodingOptions[TReference] {
	return &model_starlark.ValueDecodingOptions[TReference]{
		Context:         ctx,
		Readers:         &c.valueReaders,
		LabelCreator:    labelCreator,
		BzlFileBuiltins: c.bzlFileBuiltins,
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

	GetBuiltinsModuleNamesValue(key *model_analysis_pb.BuiltinsModuleNames_Key) model_core.Message[*model_analysis_pb.BuiltinsModuleNames_Value, TReference]
	GetCompiledBzlFileDecodedGlobalsValue(key *model_analysis_pb.CompiledBzlFileDecodedGlobals_Key) (starlark.StringDict, bool)
}

func (baseComputer[TReference, TMetadata]) loadBzlGlobals(e loadBzlGlobalsEnvironment[TReference], canonicalPackage label.CanonicalPackage, loadLabelStr string, builtinsModuleNames []string) (starlark.StringDict, error) {
	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	if !allBuiltinsModulesNames.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	apparentLoadLabel, err := canonicalPackage.AppendLabel(loadLabelStr)
	if err != nil {
		return nil, fmt.Errorf("invalid label %#v in load() statement: %w", loadLabelStr, err)
	}
	canonicalRepo := canonicalPackage.GetCanonicalRepo()
	canonicalLoadLabel, err := label.Canonicalize(newLabelResolver(e), canonicalRepo, apparentLoadLabel)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve label %#v in load() statement: %w", apparentLoadLabel.String(), err)
	}
	decodedGlobals, ok := e.GetCompiledBzlFileDecodedGlobalsValue(&model_analysis_pb.CompiledBzlFileDecodedGlobals_Key{
		Label:               canonicalLoadLabel.String(),
		BuiltinsModuleNames: builtinsModuleNames,
	})
	if !ok {
		return nil, evaluation.ErrMissingDependency
	}
	return decodedGlobals, nil
}

func (c *baseComputer[TReference, TMetadata]) loadBzlGlobalsInStarlarkThread(e loadBzlGlobalsEnvironment[TReference], thread *starlark.Thread, loadLabelStr string, builtinsModuleNames []string) (starlark.StringDict, error) {
	return c.loadBzlGlobals(e, util.Must(label.NewCanonicalLabel(thread.CallFrame(0).Pos.Filename())).GetCanonicalPackage(), loadLabelStr, builtinsModuleNames)
}

func (c *baseComputer[TReference, TMetadata]) preloadBzlGlobals(e loadBzlGlobalsEnvironment[TReference], canonicalPackage label.CanonicalPackage, program *starlark.Program, builtinsModuleNames []string) (aggregateErr error) {
	numLoads := program.NumLoads()
	for i := 0; i < numLoads; i++ {
		loadLabelStr, _ := program.Load(i)
		if _, err := c.loadBzlGlobals(e, canonicalPackage, loadLabelStr, builtinsModuleNames); err != nil {
			if !errors.Is(err, evaluation.ErrMissingDependency) {
				return err
			}
			aggregateErr = err
		}
	}
	return aggregateErr
}

type starlarkThreadEnvironment[TReference any] interface {
	loadBzlGlobalsEnvironment[TReference]
	GetCompiledBzlFileFunctionFactoryValue(*model_analysis_pb.CompiledBzlFileFunctionFactory_Key) (*starlark.FunctionFactory, bool)
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

func (c *baseComputer[TReference, TMetadata]) newStarlarkThread(ctx context.Context, e starlarkThreadEnvironment[TReference], builtinsModuleNames []string) *starlark.Thread {
	thread := &starlark.Thread{
		// TODO: Provide print method.
		Print: nil,
		Load: func(thread *starlark.Thread, loadLabelStr string) (starlark.StringDict, error) {
			return c.loadBzlGlobalsInStarlarkThread(e, thread, loadLabelStr, builtinsModuleNames)
		},
		Steps: 1000,
	}

	thread.SetLocal(model_starlark.LabelResolverKey, newLabelResolver(e))
	thread.SetLocal(model_starlark.FunctionFactoryResolverKey, func(filename label.CanonicalLabel) (*starlark.FunctionFactory, error) {
		// Prevent modules containing builtin Starlark code from
		// depending on itself.
		functionFactory, gotFunctionFactory := e.GetCompiledBzlFileFunctionFactoryValue(&model_analysis_pb.CompiledBzlFileFunctionFactory_Key{
			Label:               filename.String(),
			BuiltinsModuleNames: trimBuiltinModuleNames(builtinsModuleNames, filename.GetCanonicalRepo().GetModuleInstance().GetModule()),
		})
		if !gotFunctionFactory {
			return nil, evaluation.ErrMissingDependency
		}
		return functionFactory, nil
	})
	thread.SetLocal(
		model_starlark.ValueDecodingOptionsKey,
		c.getValueDecodingOptions(ctx, func(resolvedLabel label.ResolvedLabel) (starlark.Value, error) {
			return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
		}),
	)
	return thread
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

	thread := c.newStarlarkThread(ctx, e, buildSpecification.BuiltinsModuleNames)
	missingDependencies := false
	labelResolver := newLabelResolver(e)
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

		for _, targetPattern := range key.TargetPatterns {
			apparentTargetPattern, err := label.NewApparentTargetPattern(targetPattern)
			if err != nil {
				return PatchedBuildResultValue[TMetadata]{}, fmt.Errorf("invalid target pattern %#v: %w", targetPattern, err)
			}
			canonicalTargetPattern, err := label.Canonicalize(labelResolver, rootRepo, apparentTargetPattern)
			if err != nil {
				return PatchedBuildResultValue[TMetadata]{}, err
			}

			var iterErr error
			for canonicalTargetLabel := range c.expandCanonicalTargetPattern(
				ctx,
				e,
				canonicalTargetPattern,
				/* includeManualTargets = */ false,
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
					return PatchedBuildResultValue[TMetadata]{}, fmt.Errorf("failed to iterate target pattern %#v: %w", targetPattern, iterErr)
				}
				missingDependencies = true
			}
		}
	}
	if missingDependencies {
		return PatchedBuildResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	return PatchedBuildResultValue[TMetadata]{}, errors.New("TODO: report build results in a meaningful way")
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

	fileReader, gotFileReader := e.GetFileReaderValue(&model_analysis_pb.FileReader_Key{})
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
