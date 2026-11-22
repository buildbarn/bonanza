package analysis

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"bonanza.build/pkg/dotbazel"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	"bonanza.build/pkg/starlark/eval"
)

// repoOverridesGetter is called to obtain the overrides that the root
// module declares for repos of a given module extension, using
// override_repo() and inject_repo(). Keys of the resulting map
// correspond to the names of repos as seen from within the module
// extension.
type repoOverridesGetter func(moduleExtension label.ModuleExtension) (map[label.ApparentRepo]label.CanonicalRepo, error)

// noRepoOverrides is an implementation of repoOverridesGetter that
// can be used to resolve repos while disregarding any overrides.
func noRepoOverrides(label.ModuleExtension) (map[label.ApparentRepo]label.CanonicalRepo, error) {
	return nil, nil
}

// newRepoOverridesGetter returns an implementation of
// repoOverridesGetter that obtains overrides by calling into the
// ModuleExtensionRepoOverrides function.
func newRepoOverridesGetter[TReference any](e ModuleExtensionRepoOverridesValueGetter[TReference]) repoOverridesGetter {
	cache := map[label.ModuleExtension]map[label.ApparentRepo]label.CanonicalRepo{}
	return func(moduleExtension label.ModuleExtension) (map[label.ApparentRepo]label.CanonicalRepo, error) {
		if overrides, ok := cache[moduleExtension]; ok {
			return overrides, nil
		}
		overridesValue := e.GetModuleExtensionRepoOverridesValue(&model_analysis_pb.ModuleExtensionRepoOverrides_Key{
			ModuleExtension: moduleExtension.String(),
		})
		if !overridesValue.IsSet() {
			return nil, evaluation.ErrMissingDependency
		}
		overrides := make(map[label.ApparentRepo]label.CanonicalRepo, len(overridesValue.Message.Overrides))
		for _, override := range overridesValue.Message.Overrides {
			fromApparentRepo, err := label.NewApparentRepo(override.FromApparentRepo)
			if err != nil {
				return nil, fmt.Errorf("invalid apparent repo %#v: %w", override.FromApparentRepo, err)
			}
			toCanonicalRepo, err := label.NewCanonicalRepo(override.ToCanonicalRepo)
			if err != nil {
				return nil, fmt.Errorf("invalid canonical repo %#v: %w", override.ToCanonicalRepo, err)
			}
			overrides[fromApparentRepo] = toCanonicalRepo
		}
		cache[moduleExtension] = overrides
		return overrides, nil
	}
}

type repoMapping interface {
	toCanonicalRepo(others map[label.ApparentRepo]repoMapping, getRepoOverrides repoOverridesGetter) (label.CanonicalRepo, error)
}

type canonicalRepoMapping struct {
	canonicalRepo label.CanonicalRepo
}

func (rm canonicalRepoMapping) toCanonicalRepo(others map[label.ApparentRepo]repoMapping, getRepoOverrides repoOverridesGetter) (label.CanonicalRepo, error) {
	return rm.canonicalRepo, nil
}

// moduleExtensionReference refers to a module extension, using the
// arguments that were provided to use_extension().
type moduleExtensionReference struct {
	extensionBzlFile label.ApparentLabel
	extensionName    label.StarlarkIdentifier
}

// getModuleExtension resolves the name of the module extension, using
// the repos that are visible to the module that called use_extension().
func (me moduleExtensionReference) getModuleExtension(others map[label.ApparentRepo]repoMapping) (label.ModuleExtension, error) {
	var badModuleExtension label.ModuleExtension
	canonicalExtensionBzlFile, ok := me.extensionBzlFile.AsCanonical()
	if !ok {
		extensionApparentRepo, ok := me.extensionBzlFile.GetApparentRepo()
		if !ok {
			return badModuleExtension, fmt.Errorf("extension .bzl file %#v uses @@ to refer to the root module, which is not permitted in this context", me.extensionBzlFile.String())
		}
		extensionRepoMapping, ok := others[extensionApparentRepo]
		if !ok {
			return badModuleExtension, fmt.Errorf("extension .bzl file %#v uses unknown repo %#v", me.extensionBzlFile.String(), extensionApparentRepo.String())
		}
		extensionCanonicalRepoMapping, ok := extensionRepoMapping.(canonicalRepoMapping)
		if !ok {
			return badModuleExtension, fmt.Errorf("extension .bzl file %#v uses repo %#v, which is part of a module extension", me.extensionBzlFile.String(), extensionApparentRepo.String())
		}
		canonicalExtensionBzlFile = me.extensionBzlFile.WithCanonicalRepo(extensionCanonicalRepoMapping.canonicalRepo)
	}
	return canonicalExtensionBzlFile.
		AppendStarlarkIdentifier(me.extensionName).
		ToModuleExtension(), nil
}

type moduleExtensionRepoMapping struct {
	moduleExtensionReference
	toApparentRepo label.ApparentRepo
}

func (rm moduleExtensionRepoMapping) toCanonicalRepo(others map[label.ApparentRepo]repoMapping, getRepoOverrides repoOverridesGetter) (label.CanonicalRepo, error) {
	var badCanonicalRepo label.CanonicalRepo
	moduleExtension, err := rm.getModuleExtension(others)
	if err != nil {
		return badCanonicalRepo, err
	}

	// If the root module has overridden the repo using
	// override_repo() or inject_repo(), refer to the repo that
	// is used instead.
	overrides, err := getRepoOverrides(moduleExtension)
	if err != nil {
		return badCanonicalRepo, err
	}
	if canonicalRepo, ok := overrides[rm.toApparentRepo]; ok {
		return canonicalRepo, nil
	}
	return moduleExtension.GetCanonicalRepoWithModuleExtension(rm.toApparentRepo), nil
}

// declaredRepoOverride contains the arguments of a single repo that
// was provided to override_repo() or inject_repo().
type declaredRepoOverride struct {
	moduleExtensionReference

	// The name of the repo, as seen from within the module
	// extension.
	fromApparentRepo label.ApparentRepo
	// The name of the repo, as seen from within the current
	// module, that should be used instead.
	toApparentRepo label.ApparentRepo
	// Whether the repo was provided to override_repo() (true) or
	// inject_repo() (false).
	mustExist bool
}

type repoMappingCapturingModuleDotBazelHandler struct {
	moduleInstance              label.ModuleInstance
	modulesWithMultipleVersions map[label.Module]OverrideVersions
	ignoreDevDependencies       bool

	repos         map[label.ApparentRepo]repoMapping
	repoOverrides []declaredRepoOverride

	// Repo names of modules on which bazel_dep() has been called,
	// keyed by the module instance to which they resolve. This is
	// used to detect that a module depends on the same module
	// instance multiple times.
	dependencyRepoNames map[label.ModuleInstance]label.ApparentRepo
}

func newRepoMappingCapturingModuleDotBazelHandler(moduleInstance label.ModuleInstance, modulesWithMultipleVersions map[label.Module]OverrideVersions, ignoreDevDependencies bool) *repoMappingCapturingModuleDotBazelHandler {
	return &repoMappingCapturingModuleDotBazelHandler{
		moduleInstance:              moduleInstance,
		modulesWithMultipleVersions: modulesWithMultipleVersions,
		ignoreDevDependencies:       ignoreDevDependencies,

		repos:               map[label.ApparentRepo]repoMapping{},
		dependencyRepoNames: map[label.ModuleInstance]label.ApparentRepo{},
	}
}

func (h *repoMappingCapturingModuleDotBazelHandler) setRepo(from label.ApparentRepo, to repoMapping) error {
	if _, ok := h.repos[from]; ok {
		return fmt.Errorf("multiple repos declared with name %#v", from.String())
	}
	h.repos[from] = to
	return nil
}

func (h *repoMappingCapturingModuleDotBazelHandler) BazelDep(name label.Module, version *label.ModuleVersion, repoName *label.ApparentRepo, devDependency bool) error {
	if devDependency && h.ignoreDevDependencies {
		return nil
	}
	if repoName == nil {
		// bazel_dep(repo_name = None) does not cause the
		// module to become visible.
		return nil
	}
	versions := h.modulesWithMultipleVersions[name]
	var nearestVersion *label.ModuleVersion
	if len(versions) > 0 {
		v, err := versions.LookupNearestVersion(version)
		if err != nil {
			return fmt.Errorf("dependency on module %#v: %w", name.String(), err)
		}
		nearestVersion = &v
	}

	// Depending on the same module instance multiple times is
	// not permitted, as it would cause the same repo to be
	// visible under multiple names. Only if
	// multiple_version_override() is used, it is possible to
	// depend on the same module multiple times, as long as the
	// dependencies resolve to different versions.
	moduleInstance := name.ToModuleInstance(nearestVersion)
	if otherRepoName, ok := h.dependencyRepoNames[moduleInstance]; ok {
		if nearestVersion == nil {
			return fmt.Errorf(
				"module depends on module %#v at least twice (with repo names %#v and %#v); consider adding a multiple_version_override() if you want to depend on multiple versions of this module simultaneously",
				name.String(),
				otherRepoName.String(),
				repoName.String(),
			)
		}
		return fmt.Errorf(
			"module depends on version %s of module %#v at least twice (with repo names %#v and %#v)",
			*nearestVersion,
			name.String(),
			otherRepoName.String(),
			repoName.String(),
		)
	}
	h.dependencyRepoNames[moduleInstance] = *repoName

	return h.setRepo(*repoName, canonicalRepoMapping{
		canonicalRepo: moduleInstance.GetBareCanonicalRepo(),
	})
}

func (repoMappingCapturingModuleDotBazelHandler) FlagAlias(name string, starlarkFlag label.ApparentLabel) error {
	return nil
}

func (h *repoMappingCapturingModuleDotBazelHandler) Module(name label.Module, repoName label.ApparentRepo, bazelCompatibility []string) error {
	return h.setRepo(repoName, canonicalRepoMapping{
		canonicalRepo: h.moduleInstance.GetBareCanonicalRepo(),
	})
}

func (repoMappingCapturingModuleDotBazelHandler) RegisterExecutionPlatforms(platformTargetPatterns []label.ApparentTargetPattern, devDependency bool) error {
	return nil
}

func (repoMappingCapturingModuleDotBazelHandler) RegisterToolchains(toolchainTargetPatterns []label.ApparentTargetPattern, devDependency bool) error {
	return nil
}

func (h *repoMappingCapturingModuleDotBazelHandler) UseExtension(extensionBzlFile label.ApparentLabel, extensionName label.StarlarkIdentifier, devDependency, isolate bool) (dotbazel.ModuleExtensionProxy, error) {
	if !devDependency || !h.ignoreDevDependencies {
		return repoMappingCapturingModuleExtensionProxy{
			handler: h,
			moduleExtensionReference: moduleExtensionReference{
				extensionBzlFile: extensionBzlFile,
				extensionName:    extensionName,
			},
		}, nil
	}
	return dotbazel.NullModuleExtensionProxy, nil
}

func (h *repoMappingCapturingModuleDotBazelHandler) UseRepoRule(repoRuleBzlFile label.ApparentLabel, repoRuleName label.StarlarkIdentifier) (dotbazel.RepoRuleProxy, error) {
	return func(thread *eval.Thread, name label.ApparentRepo, devDependency bool, attrs map[string]eval.Value) error {
		if devDependency && h.ignoreDevDependencies {
			return nil
		}
		return h.setRepo(name, canonicalRepoMapping{
			canonicalRepo: h.moduleInstance.
				GetRepoRulesModuleExtension().
				GetCanonicalRepoWithModuleExtension(name),
		})
	}, nil
}

type repoMappingCapturingModuleExtensionProxy struct {
	moduleExtensionReference
	handler *repoMappingCapturingModuleDotBazelHandler
}

func (repoMappingCapturingModuleExtensionProxy) Tag(thread *eval.Thread, className string, attrs map[string]eval.Value) error {
	return nil
}

func (p repoMappingCapturingModuleExtensionProxy) UseRepo(repos map[label.ApparentRepo]label.ApparentRepo) error {
	for fromApparentRepo := range repos {
		if err := p.handler.setRepo(fromApparentRepo, moduleExtensionRepoMapping{
			moduleExtensionReference: p.moduleExtensionReference,
			toApparentRepo:           repos[fromApparentRepo],
		}); err != nil {
			return err
		}
	}
	return nil
}

func (p repoMappingCapturingModuleExtensionProxy) addRepoOverrides(repos map[label.ApparentRepo]label.ApparentRepo, mustExist bool) error {
	// override_repo() and inject_repo() only have an effect in the
	// root module, and are ignored if dev dependencies are ignored.
	if p.handler.ignoreDevDependencies {
		return nil
	}
	for _, fromApparentRepo := range slices.SortedFunc(
		maps.Keys(repos),
		func(a, b label.ApparentRepo) int { return strings.Compare(a.String(), b.String()) },
	) {
		p.handler.repoOverrides = append(p.handler.repoOverrides, declaredRepoOverride{
			moduleExtensionReference: p.moduleExtensionReference,
			fromApparentRepo:         fromApparentRepo,
			toApparentRepo:           repos[fromApparentRepo],
			mustExist:                mustExist,
		})
	}
	return nil
}

// OverrideRepo is called when override_repo() is invoked. The
// overrides are captured, so that they can be used by
// ComputeModuleExtensionRepoOverridesValue().
func (p repoMappingCapturingModuleExtensionProxy) OverrideRepo(repos map[label.ApparentRepo]label.ApparentRepo) error {
	return p.addRepoOverrides(repos, true)
}

// InjectRepo is called when inject_repo() is invoked. Similar to
// OverrideRepo(), the overrides are captured.
func (p repoMappingCapturingModuleExtensionProxy) InjectRepo(repos map[label.ApparentRepo]label.ApparentRepo) error {
	return p.addRepoOverrides(repos, false)
}

// getRepoOverrides returns the overrides that the module declares for
// repos of a given module extension, using override_repo() and
// inject_repo(). The repos used as overrides are resolved without
// taking any overrides into account.
func (h *repoMappingCapturingModuleDotBazelHandler) getRepoOverrides(moduleExtension label.ModuleExtension) ([]*model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override, error) {
	// Resolve the module extensions of all overrides. Even
	// though we only return overrides for a single module
	// extension, we need to know about overrides of other
	// module extensions to reject chains of overrides.
	type overriddenRepo struct {
		moduleExtension  label.ModuleExtension
		fromApparentRepo label.ApparentRepo
	}
	overriddenRepos := map[overriddenRepo]struct{}{}
	var matchingOverrides []declaredRepoOverride
	for _, override := range h.repoOverrides {
		overrideModuleExtension, err := override.getModuleExtension(h.repos)
		if err != nil {
			return nil, err
		}
		key := overriddenRepo{
			moduleExtension:  overrideModuleExtension,
			fromApparentRepo: override.fromApparentRepo,
		}
		if _, ok := overriddenRepos[key]; ok {
			return nil, fmt.Errorf("repo %#v of module extension %#v is overridden multiple times", override.fromApparentRepo.String(), overrideModuleExtension.String())
		}
		overriddenRepos[key] = struct{}{}
		if overrideModuleExtension == moduleExtension {
			matchingOverrides = append(matchingOverrides, override)
		}
	}

	overrides := make([]*model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override, 0, len(matchingOverrides))
	for _, override := range matchingOverrides {
		target, ok := h.repos[override.toApparentRepo]
		if !ok {
			return nil, fmt.Errorf("repo %#v of module extension %#v is overridden with repo %#v, but no repo is visible under that name", override.fromApparentRepo.String(), moduleExtension.String(), override.toApparentRepo.String())
		}
		if targetModuleExtensionRepo, ok := target.(moduleExtensionRepoMapping); ok {
			targetModuleExtension, err := targetModuleExtensionRepo.getModuleExtension(h.repos)
			if err != nil {
				return nil, err
			}
			if _, ok := overriddenRepos[overriddenRepo{
				moduleExtension:  targetModuleExtension,
				fromApparentRepo: targetModuleExtensionRepo.toApparentRepo,
			}]; ok {
				return nil, fmt.Errorf("repo %#v of module extension %#v is overridden with repo %#v, which is overridden itself", override.fromApparentRepo.String(), moduleExtension.String(), override.toApparentRepo.String())
			}
		}
		canonicalRepo, err := target.toCanonicalRepo(h.repos, noRepoOverrides)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve repo %#v: %w", override.toApparentRepo.String(), err)
		}
		overrides = append(overrides, &model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override{
			FromApparentRepo: override.fromApparentRepo.String(),
			ToCanonicalRepo:  canonicalRepo.String(),
			MustExist:        override.mustExist,
		})
	}
	slices.SortFunc(overrides, func(a, b *model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override) int {
		return strings.Compare(a.FromApparentRepo, b.FromApparentRepo)
	})
	return overrides, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeModuleRepoMappingValue(ctx context.Context, key *model_analysis_pb.ModuleRepoMapping_Key, e ModuleRepoMappingEnvironment[TReference, TMetadata]) (PatchedModuleRepoMappingValue[TMetadata], error) {
	moduleInstance, err := label.NewModuleInstance(key.ModuleInstance)
	if err != nil {
		return PatchedModuleRepoMappingValue[TMetadata]{}, fmt.Errorf("invalid module instance %#v: %w", key.ModuleInstance, err)
	}

	rootModuleValue := e.GetRootModuleValue(&model_analysis_pb.RootModule_Key{})
	modulesWithMultipleVersions, gotModulesWithMultipleVersions := e.GetModulesWithMultipleVersionsObject(&model_analysis_pb.ModulesWithMultipleVersions_Key{})
	if !rootModuleValue.IsSet() || !gotModulesWithMultipleVersions {
		return PatchedModuleRepoMappingValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	handler := newRepoMappingCapturingModuleDotBazelHandler(
		moduleInstance,
		modulesWithMultipleVersions,
		rootModuleValue.Message.IgnoreRootModuleDevDependencies ||
			moduleInstance.GetModule().String() != rootModuleValue.Message.RootModuleName,
	)
	if err := c.parseActiveModuleInstanceModuleDotBazel(ctx, moduleInstance, e, dotbazel.NewOverrideIgnoringRootHandler(handler)); err != nil {
		return PatchedModuleRepoMappingValue[TMetadata]{}, err
	}

	repos := handler.repos
	getRepoOverrides := newRepoOverridesGetter(e)
	mappings := make([]*model_analysis_pb.RepoMapping, 0, len(repos))
	missingDependencies := false
	for _, apparentRepo := range slices.SortedFunc(
		maps.Keys(repos),
		func(a, b label.ApparentRepo) int { return strings.Compare(a.String(), b.String()) },
	) {
		canonicalRepo, err := repos[apparentRepo].toCanonicalRepo(repos, getRepoOverrides)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
			return PatchedModuleRepoMappingValue[TMetadata]{}, fmt.Errorf("failed to create mapping for repo %#v: %s", apparentRepo.String(), err)
		}
		fromApparentRepo := apparentRepo.String()
		mappings = append(mappings, newRepoMapping(
			fromApparentRepo,
			canonicalRepo.String(),
			getModuleRepoMappingDefault(fromApparentRepo),
		))
	}
	if missingDependencies {
		return PatchedModuleRepoMappingValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ModuleRepoMapping_Value{
		Mappings: mappings,
	}), nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeModuleExtensionRepoOverridesValue(ctx context.Context, key *model_analysis_pb.ModuleExtensionRepoOverrides_Key, e ModuleExtensionRepoOverridesEnvironment[TReference, TMetadata]) (PatchedModuleExtensionRepoOverridesValue[TMetadata], error) {
	moduleExtension, err := label.NewModuleExtension(key.ModuleExtension)
	if err != nil {
		return PatchedModuleExtensionRepoOverridesValue[TMetadata]{}, fmt.Errorf("invalid module extension %#v: %w", key.ModuleExtension, err)
	}

	rootModuleValue := e.GetRootModuleValue(&model_analysis_pb.RootModule_Key{})
	modulesWithMultipleVersions, gotModulesWithMultipleVersions := e.GetModulesWithMultipleVersionsObject(&model_analysis_pb.ModulesWithMultipleVersions_Key{})
	if !rootModuleValue.IsSet() || !gotModulesWithMultipleVersions {
		return PatchedModuleExtensionRepoOverridesValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	rootModuleName, err := label.NewModule(rootModuleValue.Message.RootModuleName)
	if err != nil {
		return PatchedModuleExtensionRepoOverridesValue[TMetadata]{}, fmt.Errorf("invalid root module name %#v: %w", rootModuleValue.Message.RootModuleName, err)
	}

	// Overrides are only respected if declared by the root
	// module. Parse its MODULE.bazel file to capture both the
	// overrides and the repos they refer to. This prevents a
	// cyclic dependency on the repo mapping of the root module,
	// which itself depends on the overrides.
	rootModuleInstance := rootModuleName.ToModuleInstance(nil)
	handler := newRepoMappingCapturingModuleDotBazelHandler(
		rootModuleInstance,
		modulesWithMultipleVersions,
		rootModuleValue.Message.IgnoreRootModuleDevDependencies,
	)
	if err := c.parseActiveModuleInstanceModuleDotBazel(ctx, rootModuleInstance, e, dotbazel.NewOverrideIgnoringRootHandler(handler)); err != nil {
		return PatchedModuleExtensionRepoOverridesValue[TMetadata]{}, err
	}

	overrides, err := handler.getRepoOverrides(moduleExtension)
	if err != nil {
		return PatchedModuleExtensionRepoOverridesValue[TMetadata]{}, err
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ModuleExtensionRepoOverrides_Value{
		Overrides: overrides,
	}), nil
}
