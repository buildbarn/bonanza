package analysis

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	pg_starlark "bonanza.build/pkg/starlark"

	"go.starlark.net/starlark"
)

type repoMapping interface {
	toProto(from label.ApparentRepo, others map[label.ApparentRepo]repoMapping) (*model_analysis_pb.ModuleRepoMapping_Value_Mapping, error)
}

type canonicalRepoMapping struct {
	canonicalRepo label.CanonicalRepo
}

func (rm canonicalRepoMapping) toProto(fromApparentRepo label.ApparentRepo, others map[label.ApparentRepo]repoMapping) (*model_analysis_pb.ModuleRepoMapping_Value_Mapping, error) {
	return &model_analysis_pb.ModuleRepoMapping_Value_Mapping{
		FromApparentRepo: fromApparentRepo.String(),
		ToCanonicalRepo:  rm.canonicalRepo.String(),
	}, nil
}

type moduleExtensionRepoMapping struct {
	extensionBzlFile label.ApparentLabel
	extensionName    label.StarlarkIdentifier
	toApparentRepo   label.ApparentRepo
}

func (rm moduleExtensionRepoMapping) toProto(fromApparentRepo label.ApparentRepo, others map[label.ApparentRepo]repoMapping) (*model_analysis_pb.ModuleRepoMapping_Value_Mapping, error) {
	canonicalExtensionBzlFile, ok := rm.extensionBzlFile.AsCanonical()
	if !ok {
		extensionApparentRepo, ok := rm.extensionBzlFile.GetApparentRepo()
		if !ok {
			return nil, fmt.Errorf("extension .bzl file %#v uses @@ to refer to the root module, which is not permitted in this context", rm.extensionBzlFile.String())
		}
		extensionRepoMapping, ok := others[extensionApparentRepo]
		if !ok {
			return nil, fmt.Errorf("extension .bzl file %#v uses unknown repo %#v", rm.extensionBzlFile.String(), extensionApparentRepo.String())
		}
		extensionCanonicalRepoMapping, ok := extensionRepoMapping.(canonicalRepoMapping)
		if !ok {
			return nil, fmt.Errorf("extension .bzl file %#v uses repo %#v, which is part of a module extension", rm.extensionBzlFile.String(), extensionApparentRepo.String())
		}
		canonicalExtensionBzlFile = rm.extensionBzlFile.WithCanonicalRepo(extensionCanonicalRepoMapping.canonicalRepo)
	}

	return &model_analysis_pb.ModuleRepoMapping_Value_Mapping{
		FromApparentRepo: fromApparentRepo.String(),
		ToCanonicalRepo: canonicalExtensionBzlFile.
			AppendStarlarkIdentifier(rm.extensionName).
			ToModuleExtension().
			GetCanonicalRepoWithModuleExtension(rm.toApparentRepo).
			String(),
	}, nil
}

type repoMappingCapturingModuleDotBazelHandler struct {
	moduleInstance              label.ModuleInstance
	modulesWithMultipleVersions map[label.Module]OverrideVersions
	ignoreDevDependencies       bool

	repos map[label.ApparentRepo]repoMapping
}

func (h *repoMappingCapturingModuleDotBazelHandler) setRepo(from label.ApparentRepo, to repoMapping) error {
	if _, ok := h.repos[from]; ok {
		return fmt.Errorf("multiple repos declared with name %#v", from.String())
	}
	h.repos[from] = to
	return nil
}

func (h *repoMappingCapturingModuleDotBazelHandler) BazelDep(name label.Module, version *label.ModuleVersion, repoName label.ApparentRepo, devDependency bool) error {
	if devDependency && h.ignoreDevDependencies {
		return nil
	}
	versions := h.modulesWithMultipleVersions[name]
	var nearestVersion *label.ModuleVersion
	if len(versions) > 0 {
		v, err := versions.LookupNearestVersion(nearestVersion)
		if err != nil {
			return err
		}
		nearestVersion = &v
	}
	return h.setRepo(repoName, canonicalRepoMapping{
		canonicalRepo: name.ToModuleInstance(nearestVersion).GetBareCanonicalRepo(),
	})
}

func (h *repoMappingCapturingModuleDotBazelHandler) Module(name label.Module, version *label.ModuleVersion, repoName label.ApparentRepo, bazelCompatibility []string) error {
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

func (h *repoMappingCapturingModuleDotBazelHandler) UseExtension(extensionBzlFile label.ApparentLabel, extensionName label.StarlarkIdentifier, devDependency, isolate bool) (pg_starlark.ModuleExtensionProxy, error) {
	if !devDependency || !h.ignoreDevDependencies {
		return repoMappingCapturingModuleExtensionProxy{
			handler:          h,
			extensionBzlFile: extensionBzlFile,
			extensionName:    extensionName,
		}, nil
	}
	return pg_starlark.NullModuleExtensionProxy, nil
}

func (h *repoMappingCapturingModuleDotBazelHandler) UseRepoRule(repoRuleBzlFile label.ApparentLabel, repoRuleName label.StarlarkIdentifier) (pg_starlark.RepoRuleProxy, error) {
	return func(name label.ApparentRepo, devDependency bool, attrs map[string]starlark.Value) error {
		if devDependency && h.ignoreDevDependencies {
			return nil
		}
		return h.setRepo(name, canonicalRepoMapping{
			canonicalRepo: h.moduleInstance.
				GetModuleExtension(repoRulesExtensionName).
				GetCanonicalRepoWithModuleExtension(name),
		})
	}, nil
}

type repoMappingCapturingModuleExtensionProxy struct {
	handler          *repoMappingCapturingModuleDotBazelHandler
	extensionBzlFile label.ApparentLabel
	extensionName    label.StarlarkIdentifier
}

func (repoMappingCapturingModuleExtensionProxy) Tag(className string, attrs map[string]starlark.Value) error {
	return nil
}

func (p repoMappingCapturingModuleExtensionProxy) UseRepo(repos map[label.ApparentRepo]label.ApparentRepo) error {
	for fromApparentRepo := range repos {
		if err := p.handler.setRepo(fromApparentRepo, moduleExtensionRepoMapping{
			extensionBzlFile: p.extensionBzlFile,
			extensionName:    p.extensionName,
			toApparentRepo:   repos[fromApparentRepo],
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeModuleRepoMappingValue(ctx context.Context, key *model_analysis_pb.ModuleRepoMapping_Key, e ModuleRepoMappingEnvironment[TReference, TMetadata]) (PatchedModuleRepoMappingValue[TMetadata], error) {
	moduleInstance, err := label.NewModuleInstance(key.ModuleInstance)
	if err != nil {
		return PatchedModuleRepoMappingValue[TMetadata]{}, fmt.Errorf("invalid module instance %#v: %w", key.ModuleInstance, err)
	}

	rootModuleValue := e.GetRootModuleValue(&model_analysis_pb.RootModule_Key{})
	modulesWithMultipleVersions, gotModulesWithMultipleVersions := e.GetModulesWithMultipleVersionsObjectValue(&model_analysis_pb.ModulesWithMultipleVersionsObject_Key{})
	if !rootModuleValue.IsSet() || !gotModulesWithMultipleVersions {
		return PatchedModuleRepoMappingValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	handler := repoMappingCapturingModuleDotBazelHandler{
		moduleInstance:              moduleInstance,
		modulesWithMultipleVersions: modulesWithMultipleVersions,
		ignoreDevDependencies: rootModuleValue.Message.IgnoreRootModuleDevDependencies ||
			moduleInstance.GetModule().String() != rootModuleValue.Message.RootModuleName,

		repos: map[label.ApparentRepo]repoMapping{},
	}
	if err := c.parseActiveModuleInstanceModuleDotBazel(ctx, moduleInstance, e, pg_starlark.NewOverrideIgnoringRootModuleDotBazelHandler(&handler)); err != nil {
		return PatchedModuleRepoMappingValue[TMetadata]{}, err
	}

	repos := handler.repos
	mappings := make([]*model_analysis_pb.ModuleRepoMapping_Value_Mapping, 0, len(repos))
	for _, apparentRepo := range slices.SortedFunc(
		maps.Keys(repos),
		func(a, b label.ApparentRepo) int { return strings.Compare(a.String(), b.String()) },
	) {
		mapping, err := repos[apparentRepo].toProto(apparentRepo, repos)
		if err != nil {
			return PatchedModuleRepoMappingValue[TMetadata]{}, fmt.Errorf("failed to create mapping for repo %#v: %s", apparentRepo.String(), err)
		}
		mappings = append(mappings, mapping)
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ModuleRepoMapping_Value{
		Mappings: mappings,
	}), nil
}
