package analysis

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/buildbarn/bb-playground/pkg/evaluation"
	"github.com/buildbarn/bb-playground/pkg/label"
	model_core "github.com/buildbarn/bb-playground/pkg/model/core"
	model_analysis_pb "github.com/buildbarn/bb-playground/pkg/proto/model/analysis"
	pg_starlark "github.com/buildbarn/bb-playground/pkg/starlark"
	"github.com/buildbarn/bb-playground/pkg/storage/dag"

	"go.starlark.net/starlark"
)

type repoMapping interface {
	toProto(from label.ApparentRepo, others map[label.ApparentRepo]repoMapping) (*model_analysis_pb.ModuleRepoMapping_Value_Success_Mapping, error)
}

type canonicalRepoMapping struct {
	canonicalRepo label.CanonicalRepo
}

func (rm canonicalRepoMapping) toProto(fromApparentRepo label.ApparentRepo, others map[label.ApparentRepo]repoMapping) (*model_analysis_pb.ModuleRepoMapping_Value_Success_Mapping, error) {
	return &model_analysis_pb.ModuleRepoMapping_Value_Success_Mapping{
		FromApparentRepo: fromApparentRepo.String(),
		Target: &model_analysis_pb.ModuleRepoMapping_Value_Success_Mapping_CanonicalRepo{
			CanonicalRepo: rm.canonicalRepo.String(),
		},
	}, nil
}

type moduleExtensionRepoMapping struct {
	extensionBzlFile label.ApparentLabel
	extensionName    label.StarlarkIdentifier
	toApparentRepo   label.ApparentRepo
}

func (rm moduleExtensionRepoMapping) toProto(fromApparentRepo label.ApparentRepo, others map[label.ApparentRepo]repoMapping) (*model_analysis_pb.ModuleRepoMapping_Value_Success_Mapping, error) {
	canonicalExtensionBzlFile, ok := rm.extensionBzlFile.AsCanonicalLabel()
	if !ok {
		extensionApparentRepo, ok := rm.extensionBzlFile.GetApparentRepo()
		if !ok {
			panic("non-canonical label should have an apparent repo")
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

	return &model_analysis_pb.ModuleRepoMapping_Value_Success_Mapping{
		FromApparentRepo: fromApparentRepo.String(),
		Target: &model_analysis_pb.ModuleRepoMapping_Value_Success_Mapping_ModuleExtensionRepo_{
			ModuleExtensionRepo: &model_analysis_pb.ModuleRepoMapping_Value_Success_Mapping_ModuleExtensionRepo{
				ExtensionIdentifier: canonicalExtensionBzlFile.AppendStarlarkIdentifier(rm.extensionName).String(),
				ToApparentRepo:      rm.toApparentRepo.String(),
			},
		},
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

func (h *repoMappingCapturingModuleDotBazelHandler) BazelDep(name label.Module, version *label.ModuleVersion, maxCompatibilityLevel int, repoName label.ApparentRepo, devDependency bool) error {
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

func (h *repoMappingCapturingModuleDotBazelHandler) Module(name label.Module, version *label.ModuleVersion, compatibilityLevel int, repoName label.ApparentRepo, bazelCompatibility []string) error {
	return h.setRepo(repoName, canonicalRepoMapping{
		canonicalRepo: h.moduleInstance.GetBareCanonicalRepo(),
	})
}

func (repoMappingCapturingModuleDotBazelHandler) RegisterExecutionPlatforms(platformLabels []label.ApparentLabel, devDependency bool) error {
	return nil
}

func (repoMappingCapturingModuleDotBazelHandler) RegisterToolchains(toolchainLabels []label.ApparentLabel, devDependency bool) error {
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

func (h *repoMappingCapturingModuleDotBazelHandler) UseRepoRule(repoRuleBzlFile label.ApparentLabel, repoRuleName string) (pg_starlark.RepoRuleProxy, error) {
	return func(name label.ApparentRepo, devDependency bool, attrs map[string]starlark.Value) error {
		if devDependency && h.ignoreDevDependencies {
			return nil
		}
		return h.setRepo(name, canonicalRepoMapping{
			canonicalRepo: h.moduleInstance.
				GetModuleExtension(label.MustNewStarlarkIdentifier("_repo_rules")).
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

func (c *baseComputer) ComputeModuleRepoMappingValue(ctx context.Context, key *model_analysis_pb.ModuleRepoMapping_Key, e ModuleRepoMappingEnvironment) (PatchedModuleRepoMappingValue, error) {
	rootModuleValue := e.GetRootModuleValue(&model_analysis_pb.RootModule_Key{})
	if !rootModuleValue.IsSet() {
		return PatchedModuleRepoMappingValue{}, evaluation.ErrMissingDependency
	}

	moduleInstance, err := label.NewModuleInstance(key.ModuleInstance)
	if err != nil {
		return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](&model_analysis_pb.ModuleRepoMapping_Value{
			Result: &model_analysis_pb.ModuleRepoMapping_Value_Failure{
				Failure: fmt.Sprintf("Invalid module instance %#v: %s", key.ModuleInstance, err.Error()),
			},
		}), nil
	}

	fileReader, err := e.GetFileReaderValue(&model_analysis_pb.FileReader_Key{})
	if err != nil {
		if !errors.Is(err, evaluation.ErrMissingDependency) {
			return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](&model_analysis_pb.ModuleRepoMapping_Value{
				Result: &model_analysis_pb.ModuleRepoMapping_Value_Failure{
					Failure: err.Error(),
				},
			}), nil
		}
		return PatchedModuleRepoMappingValue{}, err
	}

	modulesWithMultipleVersions, err := e.GetModulesWithMultipleVersionsObjectValue(&model_analysis_pb.ModulesWithMultipleVersionsObject_Key{})
	if err != nil {
		if !errors.Is(err, evaluation.ErrMissingDependency) {
			return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](&model_analysis_pb.ModuleRepoMapping_Value{
				Result: &model_analysis_pb.ModuleRepoMapping_Value_Failure{
					Failure: err.Error(),
				},
			}), nil
		}
		return PatchedModuleRepoMappingValue{}, err
	}

	handler := repoMappingCapturingModuleDotBazelHandler{
		moduleInstance:              moduleInstance,
		modulesWithMultipleVersions: modulesWithMultipleVersions,
		ignoreDevDependencies: rootModuleValue.Message.IgnoreRootModuleDevDependencies ||
			moduleInstance.GetModule().String() != rootModuleValue.Message.RootModuleName,

		repos: map[label.ApparentRepo]repoMapping{},
	}
	if err := c.parseModuleInstanceModuleDotBazel(ctx, moduleInstance, e, fileReader, &handler); err != nil {
		if !errors.Is(err, evaluation.ErrMissingDependency) {
			return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](&model_analysis_pb.ModuleRepoMapping_Value{
				Result: &model_analysis_pb.ModuleRepoMapping_Value_Failure{
					Failure: err.Error(),
				},
			}), nil
		}
		return PatchedModuleRepoMappingValue{}, err
	}

	// Add "bazel_tools" implicitly.
	repos := handler.repos
	bazelTools := label.MustNewApparentRepo("bazel_tools")
	if _, ok := repos[bazelTools]; !ok {
		canonicalRepo := label.MustNewCanonicalRepo("bazel_tools+")
		repos[bazelTools] = canonicalRepoMapping{
			canonicalRepo: canonicalRepo,
		}
	}

	mappings := make([]*model_analysis_pb.ModuleRepoMapping_Value_Success_Mapping, 0, len(repos))
	for _, apparentRepo := range slices.SortedFunc(
		maps.Keys(repos),
		func(a, b label.ApparentRepo) int { return strings.Compare(a.String(), b.String()) },
	) {
		mapping, err := repos[apparentRepo].toProto(apparentRepo, repos)
		if err != nil {
			return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](&model_analysis_pb.ModuleRepoMapping_Value{
				Result: &model_analysis_pb.ModuleRepoMapping_Value_Failure{
					Failure: fmt.Sprintf("Failed to create mapping for repo %#v: %s", apparentRepo.String(), err),
				},
			}), nil
		}
		mappings = append(mappings, mapping)
	}
	return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](&model_analysis_pb.ModuleRepoMapping_Value{
		Result: &model_analysis_pb.ModuleRepoMapping_Value_Success_{
			Success: &model_analysis_pb.ModuleRepoMapping_Value_Success{
				Mappings: mappings,
			},
		},
	}), nil
}
