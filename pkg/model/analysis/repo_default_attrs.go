package analysis

import (
	"context"
	"fmt"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"

	"github.com/buildbarn/bb-storage/pkg/util"
)

func (c *baseComputer[TReference, TMetadata]) ComputeRepoDefaultAttrsValue(ctx context.Context, key *model_analysis_pb.RepoDefaultAttrs_Key, e RepoDefaultAttrsEnvironment[TReference, TMetadata]) (PatchedRepoDefaultAttrsValue[TMetadata], error) {
	starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)

	canonicalRepo, err := label.NewCanonicalRepo(key.CanonicalRepo)
	if err != nil {
		return PatchedRepoDefaultAttrsValue[TMetadata]{}, fmt.Errorf("invalid canonical repo: %w", err)
	}

	repoFileName := util.Must(label.NewTargetName("REPO.bazel"))
	repoFileProperties := e.GetFilePropertiesValue(&model_analysis_pb.FileProperties_Key{
		CanonicalRepo: canonicalRepo.String(),
		Path:          repoFileName.String(),
	})

	fileReader, gotFileReader := e.GetFileAccessParametersObject(&model_analysis_pb.FileAccessParameters_Key{})
	if !repoFileProperties.IsSet() || !gotFileReader || !gotStarlarkValueEncoders {
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
			starlarkValueEncoders.Encoder,
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
