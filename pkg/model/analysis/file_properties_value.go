package analysis

import (
	"context"
	"errors"
	"fmt"

	"bonanza.build/pkg/evaluation"
	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// reposFilePropertiesResolver resolves the properties of a file
// contained in a repo. All repos are placed in a fictive root
// directory, which allows symbolic links with targets of shape
// "../${repo}/${file}" to resolve properly.
type reposFilePropertiesResolver[TReference object.BasicReference, TMetadata model_core.CloneableReferenceMetadata] struct {
	context          context.Context
	directoryReaders *DirectoryReaders[TReference]
	environment      FilePropertiesEnvironment[TReference, TMetadata]

	currentRepo *model_filesystem.DirectoryComponentWalker[TReference]
}

var _ path.ComponentWalker = (*reposFilePropertiesResolver[object.LocalReference, model_core.CloneableReferenceMetadata])(nil)

func (r *reposFilePropertiesResolver[TReference, TMetadata]) handleRepoOnUp() (path.ComponentWalker, error) {
	r.currentRepo = nil
	return r, nil
}

func (r *reposFilePropertiesResolver[TReference, TMetadata]) setCurrentRepo(name string) error {
	repoValue := r.environment.GetRepoValue(&model_analysis_pb.Repo_Key{
		CanonicalRepo: name,
	})
	if !repoValue.IsSet() {
		return evaluation.ErrMissingDependency
	}

	r.currentRepo = model_filesystem.NewDirectoryComponentWalker[TReference](
		r.context,
		r.directoryReaders.DirectoryContents,
		r.directoryReaders.Leaves,
		r.handleRepoOnUp,
		model_core.Nested(
			repoValue,
			&model_filesystem_pb.Directory{
				Contents: &model_filesystem_pb.Directory_ContentsExternal{
					ContentsExternal: repoValue.Message.RootDirectoryReference,
				},
			},
		),
		nil,
	)
	return nil
}

func (r *reposFilePropertiesResolver[TReference, TMetadata]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	if r.currentRepo != nil {
		return r.currentRepo.OnDirectory(name)
	}
	if err := r.setCurrentRepo(name.String()); err != nil {
		return nil, err
	}
	return path.GotDirectory{
		Child:        r.currentRepo,
		IsReversible: true,
	}, nil
}

func (r *reposFilePropertiesResolver[TReference, TMetadata]) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	return path.OnTerminalViaOnDirectory(r, name)
}

func (r *reposFilePropertiesResolver[TReference, TMetadata]) OnUp() (path.ComponentWalker, error) {
	if r.currentRepo != nil {
		return r.currentRepo.OnUp()
	}
	return nil, errors.New("path escapes repositories directory")
}

func (r *reposFilePropertiesResolver[TReference, TMetadata]) getCurrentFileProperties() (model_core.Message[*model_filesystem_pb.FileProperties, TReference], error) {
	if r.currentRepo == nil {
		return model_core.Message[*model_filesystem_pb.FileProperties, TReference]{}, errors.New("path resolves to a location outside any repo")
	}
	fileProperties := r.currentRepo.GetCurrentFileProperties()
	if !fileProperties.IsSet() {
		return model_core.Message[*model_filesystem_pb.FileProperties, TReference]{}, errors.New("path resolves to a directory, while a file was expected")
	}
	return fileProperties, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeFilePropertiesValue(ctx context.Context, key *model_analysis_pb.FileProperties_Key, e FilePropertiesEnvironment[TReference, TMetadata]) (PatchedFilePropertiesValue[TMetadata], error) {
	directoryReaders, gotDirectoryReaders := e.GetDirectoryReadersValue(&model_analysis_pb.DirectoryReaders_Key{})
	if !gotDirectoryReaders {
		return PatchedFilePropertiesValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	resolver := reposFilePropertiesResolver[TReference, TMetadata]{
		context:          ctx,
		directoryReaders: directoryReaders,
		environment:      e,
	}
	if err := resolver.setCurrentRepo(key.CanonicalRepo); err != nil {
		return PatchedFilePropertiesValue[TMetadata]{}, fmt.Errorf("failed to resolve canonical repo directory: %w", err)
	}

	if err := path.Resolve(
		path.UNIXFormat.NewParser(key.Path),
		path.NewLoopDetectingScopeWalker(path.NewRelativeScopeWalker(resolver.currentRepo)),
	); err != nil {
		if status.Code(err) == codes.NotFound {
			return model_core.NewSimplePatchedMessage[TMetadata](
				&model_analysis_pb.FileProperties_Value{},
			), nil
		}
		return PatchedFilePropertiesValue[TMetadata]{}, fmt.Errorf("failed to resolve path: %w", err)
	}

	fileProperties, err := resolver.getCurrentFileProperties()
	if err != nil {
		return PatchedFilePropertiesValue[TMetadata]{}, err
	}
	patchedFileProperties := model_core.Patch(e, fileProperties)
	return model_core.NewPatchedMessage(
		&model_analysis_pb.FileProperties_Value{
			Exists: patchedFileProperties.Message,
		},
		patchedFileProperties.Patcher,
	), nil
}
