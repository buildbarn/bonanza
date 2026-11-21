package analysis

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"

	"bonanza.build/pkg/dotbazel"
	"bonanza.build/pkg/ds"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/starlark/eval"

	"github.com/buildbarn/bb-storage/pkg/util"
)

// bazelDep contains the properties of a call to bazel_dep() that are
// relevant for computing the rough build list.
type bazelDep struct {
	module label.Module
	// Version of the module. Never nil if nodep is set.
	version *label.ModuleVersion
	// Whether the dependency was declared with repo_name = None.
	nodep bool
}

type bazelDepCapturingModuleDotBazelHandler struct {
	ignoreDevDependencies bool

	// All dependencies declared by the module. Multiple
	// dependencies on the same module may be present. Whether
	// these are valid can only be determined after versions are
	// selected, which is done as part of computing the repo
	// mapping.
	dependencies []bazelDep
}

func (h *bazelDepCapturingModuleDotBazelHandler) BazelDep(name label.Module, version *label.ModuleVersion, repoName *label.ApparentRepo, devDependency bool) error {
	if !devDependency || !h.ignoreDevDependencies {
		if repoName == nil {
			// bazel_dep(repo_name = None). The only purpose of such dependencies is to
			// require a minimum version of a module. If no
			// version is provided, they have no effect,
			// which is likely a mistake.
			if version == nil {
				return fmt.Errorf("dependency on module %#v has repo_name = None, but does not specify a version number, meaning it has no effect", name.String())
			}
		}
		h.dependencies = append(h.dependencies, bazelDep{
			module:  name,
			version: version,
			nodep:   repoName == nil,
		})
	}
	return nil
}

func (bazelDepCapturingModuleDotBazelHandler) FlagAlias(name string, starlarkFlag label.ApparentLabel) error {
	return nil
}

func (bazelDepCapturingModuleDotBazelHandler) Module(name label.Module, repoName label.ApparentRepo, bazelCompatibility []string) error {
	return nil
}

func (bazelDepCapturingModuleDotBazelHandler) RegisterExecutionPlatforms(platformTargetPatterns []label.ApparentTargetPattern, devDependency bool) error {
	return nil
}

func (bazelDepCapturingModuleDotBazelHandler) RegisterToolchains(toolchainTargetPatterns []label.ApparentTargetPattern, devDependency bool) error {
	return nil
}

func (bazelDepCapturingModuleDotBazelHandler) UseExtension(extensionBzlFile label.ApparentLabel, extensionName label.StarlarkIdentifier, devDependency, isolate bool) (dotbazel.ModuleExtensionProxy, error) {
	return dotbazel.NullModuleExtensionProxy, nil
}

func (bazelDepCapturingModuleDotBazelHandler) UseRepoRule(repoRuleBzlFile label.ApparentLabel, repoRuleName label.StarlarkIdentifier) (dotbazel.RepoRuleProxy, error) {
	return func(thread *eval.Thread, name label.ApparentRepo, devDependency bool, attrs map[string]eval.Value) error {
		return nil
	}, nil
}

type moduleToCheck struct {
	name    label.Module
	version label.ModuleVersion
}

// moduleDiscoveryQueue keeps track of which modules still need to be
// processed while computing the rough build list.
//
// Dependencies declared using bazel_dep(repo_name = None), also known
// as "nodep" dependencies, should only be followed if the module is
// already part of the dependency graph by other means. Such
// dependencies are held back until a module with the same name gets
// added to the queue.
type moduleDiscoveryQueue struct {
	modulesToCheck  []moduleToCheck
	modulesSeen     map[moduleToCheck]struct{}
	moduleNamesSeen map[label.Module]struct{}

	// Nodep dependencies on modules that are not part of the
	// dependency graph yet. Modules in this map are never
	// contained in moduleNamesSeen.
	pendingNodepDependencies map[label.Module][]moduleToCheck
}

func newModuleDiscoveryQueue() moduleDiscoveryQueue {
	return moduleDiscoveryQueue{
		modulesSeen:              map[moduleToCheck]struct{}{},
		moduleNamesSeen:          map[label.Module]struct{}{},
		pendingNodepDependencies: map[label.Module][]moduleToCheck{},
	}
}

// add a module to the queue, if it has not been added previously.
func (q *moduleDiscoveryQueue) add(module moduleToCheck) {
	if _, ok := q.modulesSeen[module]; ok {
		return
	}
	q.modulesSeen[module] = struct{}{}
	q.modulesToCheck = append(q.modulesToCheck, module)

	if _, ok := q.moduleNamesSeen[module.name]; !ok {
		// The module is now part of the dependency graph,
		// meaning that nodep dependencies on it may be
		// followed.
		q.moduleNamesSeen[module.name] = struct{}{}
		pending := q.pendingNodepDependencies[module.name]
		delete(q.pendingNodepDependencies, module.name)
		for _, pendingModule := range pending {
			q.add(pendingModule)
		}
	}
}

// addNodep adds a module to the queue if another version of the same
// module is part of the dependency graph. If not, the module is added
// as soon as that is the case.
func (q *moduleDiscoveryQueue) addNodep(module moduleToCheck) {
	if _, ok := q.moduleNamesSeen[module.name]; ok {
		q.add(module)
	} else {
		q.pendingNodepDependencies[module.name] = append(q.pendingNodepDependencies[module.name], module)
	}
}

// pop the next module to process from the queue.
func (q *moduleDiscoveryQueue) pop() (moduleToCheck, bool) {
	if len(q.modulesToCheck) == 0 {
		return moduleToCheck{}, false
	}
	module := q.modulesToCheck[0]
	q.modulesToCheck = q.modulesToCheck[1:]
	return module, true
}

type roughBuildList struct {
	ds.Slice[*model_analysis_pb.BuildListModule]
}

func (l roughBuildList) Less(i, j int) bool {
	ei, ej := l.Slice[i], l.Slice[j]
	if ni, nj := ei.Name, ej.Name; ni < nj {
		return true
	} else if ni > nj {
		return false
	}

	// Compare by version, falling back to string comparison if
	// their canonical values are the same.
	if cmp := util.Must(label.NewModuleVersion(ei.Definition.GetVersion())).Compare(util.Must(label.NewModuleVersion(ej.Definition.GetVersion()))); cmp < 0 {
		return true
	} else if cmp > 0 {
		return false
	}
	return ei.Definition.GetVersion() < ej.Definition.GetVersion()
}

// OverrideVersions is a list of module versions that was provided to
// multiple_version_override().
type OverrideVersions []label.ModuleVersion

// LookupNearestVersion returns the first version in OverrideVersions
// that does not exceed the specified value. This is used to select the
// correct module version in case multiple_version_override() is used.
func (ov OverrideVersions) LookupNearestVersion(version *label.ModuleVersion) (label.ModuleVersion, error) {
	var badVersion label.ModuleVersion
	if version == nil {
		return badVersion, errors.New("module does not specify a version number, while multiple_version_override() requires a version number")
	}
	index := sort.Search(len(ov), func(i int) bool {
		return ov[i].Compare(*version) >= 0
	})
	if index >= len(ov) {
		return badVersion, fmt.Errorf("module depends on version %s, which exceeds maximum version %s", *version, ov[len(ov)-1])
	}
	return ov[index], nil
}

func parseOverridesList(overridesList []*model_analysis_pb.OverridesListModule) (map[label.Module]OverrideVersions, error) {
	modules := make(map[label.Module]OverrideVersions, len(overridesList))
	for _, module := range overridesList {
		moduleNameStr := module.Name
		moduleName, err := label.NewModule(moduleNameStr)
		if err != nil {
			return nil, fmt.Errorf("invalid module name %#v: %w", moduleNameStr, err)
		}

		versions := make([]label.ModuleVersion, 0, len(module.Versions))
		for _, versionStr := range module.Versions {
			version, err := label.NewModuleVersion(versionStr)
			if err != nil {
				return nil, fmt.Errorf("invalid version %#v for module %#v: %w", versionStr, moduleNameStr, err)
			}
			versions = append(versions, version)
		}
		modules[moduleName] = versions
	}
	return modules, nil
}

func getModuleDotBazelURL(registryURL string, module label.Module, moduleVersion label.ModuleVersion) (string, error) {
	return url.JoinPath(registryURL, "modules", module.String(), moduleVersion.String(), dotbazel.ModuleFilename)
}

// resolveModuleToCheck determines which version of a module needs to
// be processed, given the version that is provided to bazel_dep() and
// the overrides that are in place.
func resolveModuleToCheck(modulesWithOverrides map[label.Module]OverrideVersions, moduleName, dependencyName label.Module, dependencyVersion *label.ModuleVersion) (moduleToCheck, error) {
	dependency := moduleToCheck{name: dependencyName}
	if versions, ok := modulesWithOverrides[dependencyName]; !ok {
		// No override exists, meaning we need to check an
		// exact version.
		if dependencyVersion == nil {
			return moduleToCheck{}, fmt.Errorf("module %s depends on module %s without specifying a version number, while no override is in place", moduleName, dependencyName)
		}
		dependency.version = *dependencyVersion
	} else if len(versions) > 0 {
		// multiple_version_override() is used for the current
		// module. Round up to the nearest higher version
		// number.
		v, err := versions.LookupNearestVersion(dependencyVersion)
		if err != nil {
			return moduleToCheck{}, fmt.Errorf("dependency of module %s on module %s: %s", moduleName, dependencyName, err)
		}
		dependency.version = v
	}
	return dependency, nil
}

func (baseComputer[TReference, TMetadata]) ComputeModuleRoughBuildListValue(ctx context.Context, key *model_analysis_pb.ModuleRoughBuildList_Key, e ModuleRoughBuildListEnvironment[TReference, TMetadata]) (PatchedModuleRoughBuildListValue[TMetadata], error) {
	rootModuleValue := e.GetRootModuleValue(&model_analysis_pb.RootModule_Key{})
	modulesWithOverridesValue := e.GetModulesWithOverridesValue(&model_analysis_pb.ModulesWithOverrides_Key{})
	registryURLsValue := e.GetModuleRegistryUrlsValue(&model_analysis_pb.ModuleRegistryUrls_Key{})
	fileReader, gotFileReader := e.GetFileAccessParametersObject(&model_analysis_pb.FileAccessParameters_Key{})
	if !rootModuleValue.IsSet() || !modulesWithOverridesValue.IsSet() || !registryURLsValue.IsSet() || !gotFileReader {
		return PatchedModuleRoughBuildListValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Obtain the root module name. Traversal of the modules needs
	// to start there.
	rootModuleName, err := label.NewModule(rootModuleValue.Message.RootModuleName)
	if err != nil {
		return PatchedModuleRoughBuildListValue[TMetadata]{}, fmt.Errorf("invalid root module name %#v: %w", rootModuleValue.Message.RootModuleName, err)
	}

	// Obtain the list of modules for which overrides are in place.
	// For these we should not attempt to load MODULE.bazel files
	// from Bazel Central Registry (BCR).
	modulesWithOverrides, err := parseOverridesList(modulesWithOverridesValue.Message.OverridesList)
	if err != nil {
		return PatchedModuleRoughBuildListValue[TMetadata]{}, err
	}

	ignoreDevDependencies := rootModuleValue.Message.IgnoreRootModuleDevDependencies
	queue := newModuleDiscoveryQueue()
	queue.add(moduleToCheck{
		name:    rootModuleName,
		version: util.Must(label.NewModuleVersion("0")),
	})
	missingDependencies := false
	registryURLs := registryURLsValue.Message.RegistryUrls
	var buildList roughBuildList

ProcessModule:
	for {
		module, ok := queue.pop()
		if !ok {
			break
		}
		var moduleFileContents model_core.Message[*model_filesystem_pb.FileContents, TReference]
		var buildListEntry *model_analysis_pb.BuildListModule
		if versions, ok := modulesWithOverrides[module.name]; ok {
			// An override for the module exists. This means
			// that we can access its sources directly and
			// load the MODULE.bazel file contained within.
			var moduleInstance label.ModuleInstance
			if len(versions) > 1 {
				moduleInstance = module.name.ToModuleInstance(&module.version)
			} else {
				moduleInstance = module.name.ToModuleInstance(nil)
			}
			moduleFileContentsValue := e.GetModuleDotBazelContentsValue(&model_analysis_pb.ModuleDotBazelContents_Key{
				ModuleInstance: moduleInstance.String(),
			})
			if !moduleFileContentsValue.IsSet() {
				missingDependencies = true
				continue ProcessModule
			}
			moduleFileContents = model_core.Nested(moduleFileContentsValue, moduleFileContentsValue.Message.Contents)
		} else {
			// No override exists. Download the MODULE.bazel
			// file from Bazel Central Registry (BCR). We
			// don't want to download the full sources just
			// yet, as there is no guarantee that this is
			// the definitive version to load.
			for _, registryURL := range registryURLs {
				moduleFileURL, err := getModuleDotBazelURL(registryURL, module.name, module.version)
				if err != nil {
					return PatchedModuleRoughBuildListValue[TMetadata]{}, fmt.Errorf("failed to construct URL for module %s with version %s in registry %#v: %w", module.name, module.version, registryURL, err)
				}
				httpFileContents := e.GetHTTPFileContentsValue(
					&model_analysis_pb.HttpFileContents_Key{
						FetchOptions: &model_analysis_pb.HttpFetchOptions{
							Target: &model_fetch_pb.Target{
								Urls: []string{moduleFileURL},
							},
						},
					},
				)
				if !httpFileContents.IsSet() {
					missingDependencies = true
					continue ProcessModule
				}
				if httpFileContents.Message.Exists != nil {
					moduleFileContents = model_core.Nested(httpFileContents, httpFileContents.Message.Exists.Contents)
					buildListEntry = &model_analysis_pb.BuildListModule{
						Name: module.name.String(),
						Definition: &model_analysis_pb.BuildListModule_Definition{
							Version:     module.version.String(),
							RegistryUrl: registryURL,
						},
					}
					goto GotModuleFileContents
				}
			}
			return PatchedModuleRoughBuildListValue[TMetadata]{}, fmt.Errorf("module %s with version %s cannot be found in any of the provided registries", module.name, module.version)
		}

	GotModuleFileContents:
		// Load the contents of MODULE.bazel and extract all
		// calls to bazel_dep().
		moduleFileContentsEntry, err := model_filesystem.NewFileContentsEntryFromProto(
			moduleFileContents,
		)
		if err != nil {
			return PatchedModuleRoughBuildListValue[TMetadata]{}, fmt.Errorf("invalid file contents: %w", err)
		}

		moduleFileData, err := fileReader.FileReadAll(ctx, moduleFileContentsEntry, 1<<20)
		if err != nil {
			return PatchedModuleRoughBuildListValue[TMetadata]{}, err
		}

		handler := bazelDepCapturingModuleDotBazelHandler{
			ignoreDevDependencies: ignoreDevDependencies,
		}
		ignoreDevDependencies = true
		if err := dotbazel.ParseModule(
			string(moduleFileData),
			module.name.
				ToModuleInstance(nil).
				GetBareCanonicalRepo().
				GetRootPackage().
				AppendTargetName(dotbazel.ModuleTargetName),
			nil,
			dotbazel.NewOverrideIgnoringRootHandler(&handler),
		); err != nil {
			return PatchedModuleRoughBuildListValue[TMetadata]{}, formatStarlarkError(err)
		}

		if buildListEntry != nil {
			buildList.Slice = append(buildList.Slice, buildListEntry)
		}

		for _, dep := range handler.dependencies {
			dependency, err := resolveModuleToCheck(modulesWithOverrides, module.name, dep.module, dep.version)
			if err != nil {
				return PatchedModuleRoughBuildListValue[TMetadata]{}, err
			}
			if dep.nodep {
				queue.addNodep(dependency)
			} else {
				queue.add(dependency)
			}
		}
	}

	if missingDependencies {
		return PatchedModuleRoughBuildListValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	sort.Sort(buildList)
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ModuleRoughBuildList_Value{
		BuildList: buildList.Slice,
	}), nil
}
