package analysis

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strings"

	"go.starlark.net/starlark"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	pg_starlark "bonanza.build/pkg/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
)

func (c *baseComputer[TReference, TMetadata]) ComputeModulesWithRemoteOverridesValue(ctx context.Context, key *model_analysis_pb.ModulesWithRemoteOverrides_Key, e ModulesWithRemoteOverridesEnvironment[TReference, TMetadata]) (PatchedModulesWithRemoteOverridesValue[TMetadata], error) {
	rootModuleValue := e.GetRootModuleValue(&model_analysis_pb.RootModule_Key{})
	if !rootModuleValue.IsSet() {
		return PatchedModulesWithRemoteOverridesValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Obtain the root module name to find the file.
	rootModuleName, err := label.NewModule(rootModuleValue.Message.RootModuleName)
	if err != nil {
		return PatchedModulesWithRemoteOverridesValue[TMetadata]{}, fmt.Errorf("invalid root module name %#v: %w", rootModuleValue.Message.RootModuleName, err)
	}

	// Parse out the root module to find the overrides.
	overrideModules := []*model_analysis_pb.ModuleOverride{}
	handler := &overrideExtractingModuleDotBazelHandler[TReference, TMetadata]{
		overrideModules:      &overrideModules,
		valueEncodingOptions: c.getValueEncodingOptions(ctx, e, nil),
		patcher:              model_core.NewReferenceMessagePatcher[TMetadata](),
	}
	err = c.parseLocalModuleInstanceModuleDotBazel(ctx, rootModuleName.ToModuleInstance(nil), e, handler)
	if err != nil {
		return PatchedModulesWithRemoteOverridesValue[TMetadata]{}, err
	}

	// Set any overrides coming from a remote location.
	return model_core.NewPatchedMessage(
		&model_analysis_pb.ModulesWithRemoteOverrides_Value{
			ModuleOverrides: overrideModules,
		},
		handler.patcher,
	), nil
}

// overrideExtractingModuleDotBazelHandler is capable of capturing the paths
// contained in git_override(), archive_override(), single_version_override(),
// and multi_version_override() directives of a MODULE.bazel file. These paths
// are needed by server to determine which remote modules to download perform
// the build.
type overrideExtractingModuleDotBazelHandler[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	overrideModules      *[]*model_analysis_pb.ModuleOverride // Keep them in order for duplication checks and caching.
	valueEncodingOptions *model_starlark.ValueEncodingOptions[TReference, TMetadata]
	patcher              *model_core.ReferenceMessagePatcher[TMetadata]
}

func prefixToUNIXString(stripPrefix path.Parser) (string, error) {
	p, scopeWalker := path.EmptyBuilder.Join(path.VoidScopeWalker)
	if err := path.Resolve(stripPrefix, scopeWalker); err != nil {
		return "", err
	}
	return p.GetUNIXString(), nil
}

func stringersToStrings[E fmt.Stringer, T ~[]E](slice T) []string {
	strs := make([]string, len(slice))
	for idx := range slice {
		strs[idx] = slice[idx].String()
	}
	return strs
}

func (h *overrideExtractingModuleDotBazelHandler[TReference, TMetadata]) addOverrideModule(override *model_analysis_pb.ModuleOverride) error {
	idx, found := slices.BinarySearchFunc(*h.overrideModules, override, func(a, b *model_analysis_pb.ModuleOverride) int {
		return strings.Compare(a.GetName(), b.GetName())
	})
	if found {
		return fmt.Errorf("module %s already contains a module override", override.GetName())
	}
	*h.overrideModules = slices.Insert(*h.overrideModules, idx, override)
	return nil
}

func (overrideExtractingModuleDotBazelHandler[TReference, TMetadata]) GetRootModuleName() (label.Module, error) {
	return label.Module{}, nil
}

func (overrideExtractingModuleDotBazelHandler[TReference, TMetadata]) BazelDep(name label.Module, version *label.ModuleVersion, repoName label.ApparentRepo, devDependency bool) error {
	return nil
}

func (overrideExtractingModuleDotBazelHandler[TReference, TMetadata]) LocalPathOverride(moduleName label.Module, path path.Parser) error {
	return nil
}

func (overrideExtractingModuleDotBazelHandler[TReference, TMetadata]) Module(name label.Module, version *label.ModuleVersion, repoName label.ApparentRepo, bazelCompatibility []string) error {
	return nil
}

func (h *overrideExtractingModuleDotBazelHandler[TReference, TMetadata]) MultipleVersionOverride(moduleName label.Module, versions []label.ModuleVersion, registryURL *url.URL) error {
	vers := stringersToStrings(versions)
	slices.Sort(vers) // Ensure versions are sorted for caching.

	var registry string
	if registryURL != nil {
		registry = registryURL.String()
	}

	return h.addOverrideModule(&model_analysis_pb.ModuleOverride{
		Name: moduleName.String(),
		Kind: &model_analysis_pb.ModuleOverride_MultipleVersions_{
			MultipleVersions: &model_analysis_pb.ModuleOverride_MultipleVersions{
				Versions: vers,
				Registry: registry,
			},
		},
	})
}

func (overrideExtractingModuleDotBazelHandler[TReference, TMetadata]) RegisterExecutionPlatforms(platformLabels []label.ApparentTargetPattern, devDependency bool) error {
	return nil
}

func (overrideExtractingModuleDotBazelHandler[TReference, TMetadata]) RegisterToolchains(toolchainLabels []label.ApparentTargetPattern, devDependency bool) error {
	return nil
}

func (h *overrideExtractingModuleDotBazelHandler[TReference, TMetadata]) RepositoryRuleOverride(moduleName label.Module, repositoryRuleIdentifier label.CanonicalStarlarkIdentifier, attrs map[string]starlark.Value) error {
	attrNames := slices.Collect(maps.Keys(attrs))
	sort.Strings(attrNames)

	attrsMap := make(map[string]any, len(attrs))
	for key, value := range attrs {
		attrsMap[key] = value
	}
	fields, _, err := model_starlark.NewStructFromDict[TReference, TMetadata](nil, attrsMap).
		EncodeStructFields(map[starlark.Value]struct{}{}, h.valueEncodingOptions)
	if err != nil {
		return err
	}
	h.patcher.Merge(fields.Patcher)

	return h.addOverrideModule(&model_analysis_pb.ModuleOverride{
		Name: moduleName.String(),
		Kind: &model_analysis_pb.ModuleOverride_RepositoryRule{
			RepositoryRule: &model_starlark_pb.Repo_Definition{
				RepositoryRuleIdentifier: repositoryRuleIdentifier.String(),
				AttrValues:               fields.Message,
			},
		},
	})
}

func (h *overrideExtractingModuleDotBazelHandler[TReference, TMetadata]) SingleVersionOverride(moduleName label.Module, version *label.ModuleVersion, registryURL *url.URL, patchOptions *pg_starlark.PatchOptions) error {
	var vers, registry string
	if version != nil {
		vers = version.String()
	}
	if registryURL != nil {
		registry = registryURL.String()
	}
	return h.addOverrideModule(&model_analysis_pb.ModuleOverride{
		Name: moduleName.String(),
		Kind: &model_analysis_pb.ModuleOverride_SingleVersion_{
			SingleVersion: &model_analysis_pb.ModuleOverride_SingleVersion{
				Version:       vers,
				Registry:      registry,
				PatchLabels:   stringersToStrings(patchOptions.Patches),
				PatchCommands: patchOptions.PatchCmds,
				PatchStrip:    int64(patchOptions.PatchStrip),
			},
		},
	})
}

func (overrideExtractingModuleDotBazelHandler[TReference, TMetadata]) UseExtension(extensionBzlFile label.ApparentLabel, extensionName label.StarlarkIdentifier, devDependency, isolate bool) (pg_starlark.ModuleExtensionProxy, error) {
	return pg_starlark.NullModuleExtensionProxy, nil
}

func (overrideExtractingModuleDotBazelHandler[TReference, TMetadata]) UseRepoRule(repoRuleBzlFile label.ApparentLabel, repoRuleName label.StarlarkIdentifier) (pg_starlark.RepoRuleProxy, error) {
	return func(name label.ApparentRepo, devDependency bool, attrs map[string]starlark.Value) error {
		return nil
	}, nil
}
