package starlark

import (
	"fmt"

	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

// RepoRegistrar is used to track invocations of repository rules during
// the evaluation of module extensions.
type RepoRegistrar[TMetadata model_core.ReferenceMetadata] struct {
	repos map[string]model_core.PatchedMessage[*model_starlark_pb.Repo, TMetadata]
}

// NewRepoRegistrar creates a new RepoRegistrar that is in the initial
// state (i.e., having no registered repositories).
func NewRepoRegistrar[TMetadata model_core.ReferenceMetadata]() *RepoRegistrar[TMetadata] {
	return &RepoRegistrar[TMetadata]{
		repos: map[string]model_core.PatchedMessage[*model_starlark_pb.Repo, TMetadata]{},
	}
}

// GetRepos returns the set of repositories that have been registered by a
// previously evaluated module extension.
func (rr *RepoRegistrar[TMetadata]) GetRepos() map[string]model_core.PatchedMessage[*model_starlark_pb.Repo, TMetadata] {
	return rr.repos
}

func (rr *RepoRegistrar[TMetadata]) registerRepo(name string, repo model_core.PatchedMessage[*model_starlark_pb.Repo, TMetadata]) error {
	if _, ok := rr.repos[name]; ok {
		return fmt.Errorf("module extension contains multiple repos with name %#v", name)
	}
	rr.repos[name] = repo
	return nil
}
