package analysis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"bonanza.build/pkg/evaluation"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	pg_starlark "bonanza.build/pkg/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
)

type parseLocalModuleDotBazelEnvironment[TReference object.BasicReference] interface {
	parseModuleDotBazelFileEnvironment[TReference]
	GetFilePropertiesValue(key *model_analysis_pb.FileProperties_Key) model_core.Message[*model_analysis_pb.FileProperties_Value, TReference]
}

func (c *baseComputer[TReference, TMetadata]) parseLocalModuleInstanceModuleDotBazel(ctx context.Context, moduleInstance label.ModuleInstance, e parseLocalModuleDotBazelEnvironment[TReference], handler pg_starlark.RootModuleDotBazelHandler) error {
	// Load a file that we know exists in storage already.
	moduleFileProperties := e.GetFilePropertiesValue(&model_analysis_pb.FileProperties_Key{
		CanonicalRepo: moduleInstance.String(),
		Path:          moduleDotBazelFilename,
	})
	if !moduleFileProperties.IsSet() {
		return evaluation.ErrMissingDependency
	}
	if moduleFileProperties.Message.Exists == nil {
		return fmt.Errorf("file %#v does not exist", moduleDotBazelTargetName.String())
	}

	return c.parseModuleDotBazel(
		ctx,
		model_core.Nested(moduleFileProperties, moduleFileProperties.Message.Exists.Contents),
		moduleInstance,
		e,
		handler,
	)
}

type parseActiveModuleDotBazelEnvironment[TReference object.BasicReference] interface {
	parseModuleDotBazelFileEnvironment[TReference]
	GetModuleDotBazelContentsValue(key *model_analysis_pb.ModuleDotBazelContents_Key) model_core.Message[*model_analysis_pb.ModuleDotBazelContents_Value, TReference]
}

func (c *baseComputer[TReference, TMetadata]) parseActiveModuleInstanceModuleDotBazel(ctx context.Context, moduleInstance label.ModuleInstance, e parseActiveModuleDotBazelEnvironment[TReference], handler pg_starlark.RootModuleDotBazelHandler) error {
	// This module file might have to be loaded.
	moduleFileContentsValue := e.GetModuleDotBazelContentsValue(&model_analysis_pb.ModuleDotBazelContents_Key{
		ModuleInstance: moduleInstance.String(),
	})
	if !moduleFileContentsValue.IsSet() {
		return evaluation.ErrMissingDependency
	}
	return c.parseModuleDotBazel(
		ctx,
		model_core.Nested(moduleFileContentsValue, moduleFileContentsValue.Message.Contents),
		moduleInstance,
		e,
		handler,
	)
}

type parseModuleDotBazelFileEnvironment[TReference object.BasicReference] interface {
	GetFileReaderValue(key *model_analysis_pb.FileReader_Key) (*model_filesystem.FileReader[TReference], bool)
}

var bazelToolsModule = util.Must(label.NewModule("bazel_tools"))

func (c *baseComputer[TReference, TMetadata]) parseModuleDotBazel(ctx context.Context, moduleContentsMsg model_core.Message[*model_filesystem_pb.FileContents, TReference], moduleInstance label.ModuleInstance, e parseModuleDotBazelFileEnvironment[TReference], handler pg_starlark.RootModuleDotBazelHandler) error {
	fileReader, gotFileReader := e.GetFileReaderValue(&model_analysis_pb.FileReader_Key{})
	if !gotFileReader {
		return evaluation.ErrMissingDependency
	}

	moduleTarget := moduleInstance.GetBareCanonicalRepo().GetRootPackage().AppendTargetName(moduleDotBazelTargetName)
	moduleFileContentsEntry, err := model_filesystem.NewFileContentsEntryFromProto(
		moduleContentsMsg,
	)
	if err != nil {
		return fmt.Errorf("invalid file contents entry for file %#v: %w", moduleTarget.String(), err)
	}
	moduleFileContents, err := fileReader.FileReadAll(ctx, moduleFileContentsEntry, 1<<20)
	if err != nil {
		return err
	}

	if err := pg_starlark.ParseModuleDotBazel(
		string(moduleFileContents),
		moduleTarget,
		nil,
		handler,
	); err != nil {
		return err
	}

	if moduleInstance.GetModule() != bazelToolsModule {
		// Every module implicitly depends on "bazel_tools". Add
		// it here, so that @bazel_tools//... works and
		// bazel_tools+/MODULE.bazel also gets processed.
		if err := handler.BazelDep(
			bazelToolsModule,
			/* version = */ nil,
			/* maxCompatibilityLevel = */ 0,
			bazelToolsModule.ToApparentRepo(),
			/* devDependency = */ false,
		); err != nil {
			return err
		}
	}

	return nil
}

type visitModuleDotBazelFilesBreadthFirstEnvironment[TReference object.BasicReference] interface {
	parseActiveModuleDotBazelEnvironment[TReference]

	GetFileReaderValue(*model_analysis_pb.FileReader_Key) (*model_filesystem.FileReader[TReference], bool)
	GetModulesWithMultipleVersionsObjectValue(*model_analysis_pb.ModulesWithMultipleVersionsObject_Key) (map[label.Module]OverrideVersions, bool)
	GetRootModuleValue(*model_analysis_pb.RootModule_Key) model_core.Message[*model_analysis_pb.RootModule_Value, TReference]
}

type dependencQueueingModuleDotBazelHandler struct {
	pg_starlark.ChildModuleDotBazelHandler

	modulesWithMultipleVersions map[label.Module]OverrideVersions
	moduleInstancesToCheck      *[]label.ModuleInstance
	moduleInstancesSeen         map[label.ModuleInstance]struct{}
	ignoreDevDependencies       bool
}

func (h *dependencQueueingModuleDotBazelHandler) BazelDep(name label.Module, version *label.ModuleVersion, maxCompatibilityLevel int, repoName label.ApparentRepo, devDependency bool) error {
	if devDependency && h.ignoreDevDependencies {
		return nil
	}

	var moduleInstance label.ModuleInstance
	overrideVersions, ok := h.modulesWithMultipleVersions[name]
	if ok {
		v, err := overrideVersions.LookupNearestVersion(version)
		if err != nil {
			return fmt.Errorf("invalid dependency of module %#v: %w", name.String(), err)
		}
		moduleInstance = name.ToModuleInstance(&v)
	} else {
		moduleInstance = name.ToModuleInstance(nil)
	}

	if _, ok := h.moduleInstancesSeen[moduleInstance]; !ok {
		*h.moduleInstancesToCheck = append(*h.moduleInstancesToCheck, moduleInstance)
		h.moduleInstancesSeen[moduleInstance] = struct{}{}
	}

	return h.ChildModuleDotBazelHandler.BazelDep(name, version, maxCompatibilityLevel, repoName, devDependency)
}

func (c *baseComputer[TReference, TMetadata]) visitModuleDotBazelFilesBreadthFirst(
	ctx context.Context,
	e visitModuleDotBazelFilesBreadthFirstEnvironment[TReference],
	createHandler func(moduleInstance label.ModuleInstance, ignoreDevDependencies bool) pg_starlark.ChildModuleDotBazelHandler,
) error {
	rootModuleValue := e.GetRootModuleValue(&model_analysis_pb.RootModule_Key{})
	modulesWithMultipleVersions, gotModulesWithMultipleVersions := e.GetModulesWithMultipleVersionsObjectValue(&model_analysis_pb.ModulesWithMultipleVersionsObject_Key{})
	if !rootModuleValue.IsSet() || !gotModulesWithMultipleVersions {
		return evaluation.ErrMissingDependency
	}

	// The root module is the starting point of our traversal.
	rootModuleName, err := label.NewModule(rootModuleValue.Message.RootModuleName)
	if err != nil {
		return err
	}
	rootModuleInstance := rootModuleName.ToModuleInstance(nil)
	moduleInstancesToCheck := []label.ModuleInstance{rootModuleInstance}
	moduleInstancesSeen := map[label.ModuleInstance]struct{}{
		rootModuleInstance: {},
	}
	ignoreDevDependencies := rootModuleValue.Message.IgnoreRootModuleDevDependencies

	var finalErr error
	for len(moduleInstancesToCheck) > 0 {
		moduleInstance := moduleInstancesToCheck[0]
		moduleInstancesToCheck = moduleInstancesToCheck[1:]

		if err := c.parseActiveModuleInstanceModuleDotBazel(
			ctx,
			moduleInstance,
			e,
			pg_starlark.NewOverrideIgnoringRootModuleDotBazelHandler(&dependencQueueingModuleDotBazelHandler{
				ChildModuleDotBazelHandler:  createHandler(moduleInstance, ignoreDevDependencies),
				modulesWithMultipleVersions: modulesWithMultipleVersions,
				moduleInstancesToCheck:      &moduleInstancesToCheck,
				moduleInstancesSeen:         moduleInstancesSeen,
				ignoreDevDependencies:       ignoreDevDependencies,
			}),
		); err != nil {
			// Continue iteration if we have missing
			// dependency errors, so that we compute these
			// aggressively.
			if !errors.Is(err, evaluation.ErrMissingDependency) {
				return err
			}
			finalErr = err
		}

		ignoreDevDependencies = true
	}
	return finalErr
}

func (c *baseComputer[TReference, TMetadata]) ComputeModuleDotBazelContentsValue(ctx context.Context, key *model_analysis_pb.ModuleDotBazelContents_Key, e ModuleDotBazelContentsEnvironment[TReference, TMetadata]) (PatchedModuleDotBazelContentsValue[TMetadata], error) {
	moduleInstance, err := label.NewModuleInstance(key.ModuleInstance)
	if err != nil {
		return PatchedModuleDotBazelContentsValue[TMetadata]{}, fmt.Errorf("invalid module instance: %w", err)
	}

	canonicalRepo := moduleInstance.GetBareCanonicalRepo()
	expectedName := moduleInstance.GetModule()
	expectedNameStr := expectedName.String()
	expectedVersion, hasVersion := moduleInstance.GetModuleVersion()

	// Check to see if there is an override for this module, and if it has been loaded.
	moduleOverrides := e.GetModulesWithOverridesValue(&model_analysis_pb.ModulesWithOverrides_Key{})
	if !moduleOverrides.IsSet() {
		return PatchedModuleDotBazelContentsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	overrideList := moduleOverrides.Message.OverridesList
	if _, found := sort.Find(
		len(overrideList),
		func(i int) int {
			return strings.Compare(expectedNameStr, overrideList[i].Name)
		},
	); found { // Override found.
		// Access the MODULE.bazel file that is part of the module's sources.
		filePropertiesValue := e.GetFilePropertiesValue(&model_analysis_pb.FileProperties_Key{
			CanonicalRepo: canonicalRepo.String(),
			Path:          moduleDotBazelFilename,
		})
		if !filePropertiesValue.IsSet() {
			return PatchedModuleDotBazelContentsValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		fileContents := model_core.Patch(e, model_core.Nested(filePropertiesValue, filePropertiesValue.Message.Exists.Contents))
		return model_core.NewPatchedMessage(
			&model_analysis_pb.ModuleDotBazelContents_Value{
				Contents: fileContents.Message,
			},
			fileContents.Patcher,
		), nil
	}

	// See if the module instance is one of the resolved modules
	// that was downloaded from Bazel Central Registry. If so, we
	// prefer using the MODULE.bazel file that was downloaded
	// separately instead of the one contained in the module's
	// source archive. This prevents us from downloading and
	// extracting modules that are otherwise unused by the build.
	finalBuildListValue := e.GetModuleFinalBuildListValue(&model_analysis_pb.ModuleFinalBuildList_Key{})
	if !finalBuildListValue.IsSet() {
		return PatchedModuleDotBazelContentsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	buildList := finalBuildListValue.Message.BuildList
	if i, ok := sort.Find(
		len(buildList),
		func(i int) int {
			module := buildList[i]
			if cmp := strings.Compare(expectedNameStr, module.Name); cmp != 0 {
				return cmp
			}
			if hasVersion {
				if version, err := label.NewModuleVersion(module.Version); err == nil {
					if cmp := expectedVersion.Compare(version); cmp != 0 {
						return cmp
					}
				}
			}
			return 0
		},
	); ok {
		foundModule := buildList[i]
		foundVersion, err := label.NewModuleVersion(foundModule.Version)
		if err != nil {
			return PatchedModuleDotBazelContentsValue[TMetadata]{}, fmt.Errorf("invalid version %#v for module %#v: %w", foundModule.Version, foundModule.Name, err)
		}
		if !hasVersion || expectedVersion.Compare(foundVersion) == 0 {
			moduleFileURL, err := getModuleDotBazelURL(foundModule.RegistryUrl, expectedName, foundVersion)
			if err != nil {
				return PatchedModuleDotBazelContentsValue[TMetadata]{}, fmt.Errorf("failed to construct URL for module %s with version %s in registry %#v: %s", foundModule.Name, foundModule.Version, foundModule.RegistryUrl)
			}

			fileContentsValue := e.GetHttpFileContentsValue(
				&model_analysis_pb.HttpFileContents_Key{
					FetchOptions: &model_analysis_pb.HttpFetchOptions{
						Target: &model_fetch_pb.Target{
							Urls: []string{moduleFileURL},
						},
					},
				})
			if !fileContentsValue.IsSet() {
				return PatchedModuleDotBazelContentsValue[TMetadata]{}, evaluation.ErrMissingDependency
			}
			if fileContentsValue.Message.Exists == nil {
				return PatchedModuleDotBazelContentsValue[TMetadata]{}, fmt.Errorf("file at URL %#v does not exist", moduleFileURL)
			}
			fileContents := model_core.Patch(e, model_core.Nested(fileContentsValue, fileContentsValue.Message.Exists.Contents))
			return model_core.NewPatchedMessage(
				&model_analysis_pb.ModuleDotBazelContents_Value{
					Contents: fileContents.Message,
				},
				fileContents.Patcher,
			), nil
		}
	}

	return PatchedModuleDotBazelContentsValue[TMetadata]{}, errors.New("unknown module")
}
