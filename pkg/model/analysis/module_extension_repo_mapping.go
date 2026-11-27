package analysis

import (
	"context"
	"errors"
	"fmt"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
)

func (c *baseComputer[TReference, TMetadata]) ComputeModuleExtensionRepoMappingValue(ctx context.Context, key *model_analysis_pb.ModuleExtensionRepoMapping_Key, e ModuleExtensionRepoMappingEnvironment[TReference, TMetadata]) (PatchedModuleExtensionRepoMappingValue[TMetadata], error) {
	moduleExtension, err := label.NewModuleExtension(key.GetModuleExtension())
	if err != nil {
		return PatchedModuleExtensionRepoMappingValue[TMetadata]{}, fmt.Errorf("invalid module extension %#v: %w", key.GetModuleExtension(), err)
	}

	analysisEncoders, gotAnalysisEncoders := getAnalysisEncoders(e)
	moduleExtensionReposValue := e.GetModuleExtensionReposValue(model_analysis_pb.ModuleExtensionRepos_Key_builder{
		ModuleExtension: key.GetModuleExtension(),
	}.Build())

	// The root module may override repos declared by the module
	// extension, or inject additional ones, using override_repo()
	// and inject_repo(). Repos declared using use_repo_rule()
	// cannot be overridden.
	var overrides []*model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override
	gotOverrides := true
	if !moduleExtension.IsRepoRules() {
		moduleExtensionRepoOverridesValue := e.GetModuleExtensionRepoOverridesValue(model_analysis_pb.ModuleExtensionRepoOverrides_Key_builder{
			ModuleExtension: key.GetModuleExtension(),
		}.Build())
		gotOverrides = moduleExtensionRepoOverridesValue.IsSet()
		if gotOverrides {
			overrides = moduleExtensionRepoOverridesValue.Message.GetOverrides()
		}
	}

	if !moduleExtensionReposValue.IsSet() || !gotAnalysisEncoders || !gotOverrides {
		return PatchedModuleExtensionRepoMappingValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	var repoNames []string
	var errIter error
	for entry := range btree.AllLeaves(
		ctx,
		analysisEncoders.Readers.ModuleExtensionReposValueRepo,
		model_core.Nested(moduleExtensionReposValue, moduleExtensionReposValue.Message.GetRepos()),
		func(entry model_core.Message[*model_analysis_pb.ModuleExtensionRepos_Value_Repo, TReference]) (*model_core_pb.DecodableReference, error) {
			return entry.Message.GetParent().GetReference(), nil
		},
		&errIter,
	) {
		leaf := entry.Message.GetLeaf()
		if leaf == nil {
			return PatchedModuleExtensionRepoMappingValue[TMetadata]{}, errors.New("not a valid leaf entry")
		}
		repoNames = append(repoNames, leaf.GetName())
	}
	if errIter != nil {
		return PatchedModuleExtensionRepoMappingValue[TMetadata]{}, errIter
	}

	mappings, err := getModuleExtensionRepoMappings(moduleExtension, repoNames, overrides)
	if err != nil {
		return PatchedModuleExtensionRepoMappingValue[TMetadata]{}, err
	}

	// Store all mappings in a B-tree, as module extensions may
	// declare many repos.
	treeBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			c.chunkingParameters,
			/* isParent = */ func(mapping *model_analysis_pb.ModuleExtensionRepoMapping_Value_Mapping) bool {
				return mapping.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			analysisEncoders.Encoder,
			c.referenceFormat,
			/* parentNodeComputer = */ btree.Capturing(ctx, e, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.ModuleExtensionRepoMapping_Value_Mapping, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.ModuleExtensionRepoMapping_Value_Mapping, TMetadata] {
				var firstFromApparentRepo string
				switch childNodes.Message[0].WhichLevel() {
				case model_analysis_pb.ModuleExtensionRepoMapping_Value_Mapping_Leaf_case:
					firstFromApparentRepo = childNodes.Message[0].GetLeaf().GetFromApparentRepo()
				case model_analysis_pb.ModuleExtensionRepoMapping_Value_Mapping_Parent_case:
					firstFromApparentRepo = childNodes.Message[0].GetParent().GetFirstFromApparentRepo()
				}
				return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ModuleExtensionRepoMapping_Value_Mapping {
					return model_analysis_pb.ModuleExtensionRepoMapping_Value_Mapping_builder{
						Parent: model_analysis_pb.ModuleExtensionRepoMapping_Value_Mapping_Parent_builder{
							Reference:             patcher.AddDecodableReference(createdObject),
							FirstFromApparentRepo: firstFromApparentRepo,
						}.Build(),
					}.Build()
				})
			}),
		),
	)
	for _, mapping := range mappings {
		if err := treeBuilder.PushChild(model_core.NewSimplePatchedMessage[TMetadata](model_analysis_pb.ModuleExtensionRepoMapping_Value_Mapping_builder{
			Leaf: proto.ValueOrDefault(mapping),
		}.Build())); err != nil {
			return PatchedModuleExtensionRepoMappingValue[TMetadata]{}, err
		}
	}
	mappingsList, err := treeBuilder.FinalizeList()
	if err != nil {
		return PatchedModuleExtensionRepoMappingValue[TMetadata]{}, err
	}

	return model_core.NewPatchedMessage(
		model_analysis_pb.ModuleExtensionRepoMapping_Value_builder{
			Mappings: mappingsList.Message,
		}.Build(),
		mappingsList.Patcher,
	), nil
}

// getModuleExtensionRepoMappings merges the names of the repos declared
// by a module extension with the overrides and injected repos provided
// by the root module. Both lists must be sorted by apparent repo name.
func getModuleExtensionRepoMappings(moduleExtension label.ModuleExtension, repoNames []string, overrides []*model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override) ([]*model_analysis_pb.RepoMapping, error) {
	mappings := make([]*model_analysis_pb.RepoMapping, 0, len(repoNames)+len(overrides))
	for len(repoNames) > 0 || len(overrides) > 0 {
		if len(overrides) > 0 && (len(repoNames) == 0 || overrides[0].GetFromApparentRepo() <= repoNames[0]) {
			override := overrides[0]
			exists := len(repoNames) > 0 && override.GetFromApparentRepo() == repoNames[0]
			if override.GetMustExist() && !exists {
				return nil, fmt.Errorf("module extension %#v does not declare repo %#v, yet it is overridden using override_repo(); use inject_repo() to inject a new repo", moduleExtension.String(), override.GetFromApparentRepo())
			}
			if !override.GetMustExist() && exists {
				return nil, fmt.Errorf("module extension %#v declares repo %#v, yet it is injected using inject_repo(); use override_repo() to override an existing repo", moduleExtension.String(), override.GetFromApparentRepo())
			}
			mappings = append(mappings, newRepoMapping(
				override.GetFromApparentRepo(),
				override.GetToCanonicalRepo(),
				getModuleExtensionRepoMappingDefault(moduleExtension, override.GetFromApparentRepo()),
			))
			overrides = overrides[1:]
			if exists {
				repoNames = repoNames[1:]
			}
		} else {
			// Repo declared by the module extension, which
			// maps to the default canonical repo.
			mappings = append(mappings, model_analysis_pb.RepoMapping_builder{
				FromApparentRepo: repoNames[0],
			}.Build())
			repoNames = repoNames[1:]
		}
	}
	return mappings, nil
}
