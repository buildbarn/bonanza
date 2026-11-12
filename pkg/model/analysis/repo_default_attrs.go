package analysis

import (
	"context"
	"fmt"

	"bonanza.build/pkg/dotbazel"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
)

func (c *baseComputer[TReference, TMetadata]) ComputeRepoDefaultAttrsValue(ctx context.Context, key *model_analysis_pb.RepoDefaultAttrs_Key, e RepoDefaultAttrsEnvironment[TReference, TMetadata]) (PatchedRepoDefaultAttrsValue[TMetadata], error) {
	starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)

	canonicalRepo, err := label.NewCanonicalRepo(key.CanonicalRepo)
	if err != nil {
		return PatchedRepoDefaultAttrsValue[TMetadata]{}, fmt.Errorf("invalid canonical repo: %w", err)
	}

	repoFileProperties := e.GetFilePropertiesValue(&model_analysis_pb.FileProperties_Key{
		CanonicalRepo: canonicalRepo.String(),
		Path:          dotbazel.RepoFilename,
	})

	fileReader, gotFileReader := e.GetFileAccessParametersObject(&model_analysis_pb.FileAccessParameters_Key{})
	if !repoFileProperties.IsSet() || !gotFileReader || !gotStarlarkValueEncoders {
		return PatchedRepoDefaultAttrsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Read the contents of REPO.bazel.
	repoFileLabel := canonicalRepo.GetRootPackage().AppendTargetName(dotbazel.RepoTargetName)
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
	// Calls to ignore_directories() are discarded, as directories
	// are already ignored by the client when uploading sources.
	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.RepoDefaultAttrs_Value, error) {
		thread := eval.NewThread(ctx, eval.Universe())
		thread.Print = func(_ *eval.Thread, msg string) error {
			// TODO: Provide logging sink.
			fmt.Println(msg)
			return nil
		}
		thread.SetLocal(model_starlark.LabelResolverKey, newLabelResolver(e))

		var defaultAttrs model_core.PatchedMessage[*model_starlark_pb.InheritableAttrs, TMetadata]
		if err := dotbazel.ParseRepo(
			thread,
			string(repoFileData),
			repoFileLabel,
			dotbazel.DiscardCall,
			model_starlark.NewRepoDotBazelRepoFunction[TReference](
				starlarkValueEncoders.Encoder,
				c.chunkingParameters.GetInlinedTreeOptions(),
				e,
				&defaultAttrs,
			),
		); err != nil {
			return nil, fmt.Errorf("failed to parse %#v: %w", repoFileLabel.String(), err)
		}
		if !defaultAttrs.IsSet() {
			defaultAttrs = model_core.NewSimplePatchedMessage[TMetadata](&model_starlark.DefaultInheritableAttrs)
		}
		return &model_analysis_pb.RepoDefaultAttrs_Value{
			InheritableAttrs: defaultAttrs.Merge(patcher),
		}, nil
	})
}
