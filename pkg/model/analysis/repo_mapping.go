package analysis

import (
	"bonanza.build/pkg/label"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

// newRepoMapping creates a RepoMapping message for a given apparent
// repo. The canonical repo is omitted if it's equal to the default
// value, so that repo mappings remain compact.
func newRepoMapping(fromApparentRepo, toCanonicalRepo, defaultCanonicalRepo string) *model_analysis_pb.RepoMapping {
	if toCanonicalRepo == defaultCanonicalRepo {
		toCanonicalRepo = ""
	}
	return &model_analysis_pb.RepoMapping{
		FromApparentRepo: fromApparentRepo,
		ToCanonicalRepo:  toCanonicalRepo,
	}
}

// getRepoMappingCanonicalRepo returns the canonical repo to which a
// RepoMapping message maps, taking into account that the canonical repo
// is omitted if it's equal to the default value.
func getRepoMappingCanonicalRepo(mapping *model_analysis_pb.RepoMapping, defaultCanonicalRepo string) string {
	if mapping.ToCanonicalRepo == "" {
		return defaultCanonicalRepo
	}
	return mapping.ToCanonicalRepo
}

// getModuleRepoMappingDefault returns the canonical repo to which an
// entry in ModuleRepoMapping maps if no canonical repo is provided. This
// corresponds to the canonical repo of the module without a version
// number.
func getModuleRepoMappingDefault(fromApparentRepo string) string {
	return fromApparentRepo + "+"
}

// getModuleExtensionRepoMappingDefault returns the canonical repo to
// which an entry in ModuleExtensionRepoMapping maps if no canonical
// repo is provided. This corresponds to the canonical repo of the repo
// declared by the module extension.
func getModuleExtensionRepoMappingDefault(moduleExtension label.ModuleExtension, fromApparentRepo string) string {
	return moduleExtension.String() + "+" + fromApparentRepo
}
