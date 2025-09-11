package analysis

import (
	"context"
	"errors"
	"fmt"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

type addFilesToChangeTrackingDirectoryEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ExistingObjectCapturer[TReference, TMetadata]

	GetFilesRootValue(key model_core.PatchedMessage[*model_analysis_pb.FilesRoot_Key, TMetadata]) model_core.Message[*model_analysis_pb.FilesRoot_Value, TReference]
	GetFileRootValue(key model_core.PatchedMessage[*model_analysis_pb.FileRoot_Key, TMetadata]) model_core.Message[*model_analysis_pb.FileRoot_Value, TReference]
}

func addFilesToChangeTrackingDirectory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e addFilesToChangeTrackingDirectoryEnvironment[TReference, TMetadata],
	files model_core.Message[[]*model_starlark_pb.List_Element, TReference],
	out *changeTrackingDirectory[TReference, TMetadata],
	loadOptions *changeTrackingDirectoryLoadOptions[TReference],
	directoryLayout model_analysis_pb.DirectoryLayout,
) error {
	missingDependencies := false
	for i, element := range files.Message {
		switch level := element.Level.(type) {
		case *model_starlark_pb.List_Element_Parent_:
			patchedReference := model_core.Patch(e, model_core.Nested(files, level.Parent.Reference))
			v := e.GetFilesRootValue(
				model_core.NewPatchedMessage(
					&model_analysis_pb.FilesRoot_Key{
						ListReference:   patchedReference.Message,
						DirectoryLayout: directoryLayout,
					},
					patchedReference.Patcher,
				),
			)
			if !v.IsSet() {
				missingDependencies = true
				continue
			}
			if err := out.mergeContents(model_core.Nested(v, v.Message.RootDirectory), loadOptions); err != nil {
				return fmt.Errorf("list element at index %d: %w", i, err)
			}
		case *model_starlark_pb.List_Element_Leaf:
			file, ok := level.Leaf.Kind.(*model_starlark_pb.Value_File)
			if !ok {
				return fmt.Errorf("element at index %d is not a file", i)
			}
			if err := addFileToChangeTrackingDirectory(e, model_core.Nested(files, file.File), out, loadOptions, directoryLayout); err != nil {
				if errors.Is(err, evaluation.ErrMissingDependency) {
					missingDependencies = true
					continue
				}
				return fmt.Errorf("file at index %d: %w", i, err)
			}
		default:
			return errors.New("invalid list level type")
		}
	}
	if missingDependencies {
		return evaluation.ErrMissingDependency
	}
	return nil
}

func addFileToChangeTrackingDirectory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e addFilesToChangeTrackingDirectoryEnvironment[TReference, TMetadata],
	file model_core.Message[*model_starlark_pb.File, TReference],
	out *changeTrackingDirectory[TReference, TMetadata],
	loadOptions *changeTrackingDirectoryLoadOptions[TReference],
	directoryLayout model_analysis_pb.DirectoryLayout,
) error {
	patchedFile := model_core.Patch(e, file)
	v := e.GetFileRootValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.FileRoot_Key{
				File:            patchedFile.Message,
				DirectoryLayout: directoryLayout,
			},
			patchedFile.Patcher,
		),
	)
	if !v.IsSet() {
		return evaluation.ErrMissingDependency
	}
	if err := out.mergeContents(model_core.Nested(v, v.Message.RootDirectory), loadOptions); err != nil {
		return err
	}
	return nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeFilesRootValue(ctx context.Context, key model_core.Message[*model_analysis_pb.FilesRoot_Key, TReference], e FilesRootEnvironment[TReference, TMetadata]) (PatchedFilesRootValue[TMetadata], error) {
	directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObjectValue(&model_analysis_pb.DirectoryCreationParametersObject_Key{})
	directoryReaders, gotDirectoryReaders := e.GetDirectoryReadersValue(&model_analysis_pb.DirectoryReaders_Key{})
	if !gotDirectoryCreationParameters || !gotDirectoryReaders {
		return PatchedFilesRootValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	files, err := model_parser.Dereference(ctx, c.valueReaders.List, model_core.Nested(key, key.Message.ListReference))
	if err != nil {
		return PatchedFilesRootValue[TMetadata]{}, err
	}

	var rootDirectory changeTrackingDirectory[TReference, TMetadata]
	if err := addFilesToChangeTrackingDirectory(
		e,
		files,
		&rootDirectory,
		&changeTrackingDirectoryLoadOptions[TReference]{
			context:                 ctx,
			directoryContentsReader: directoryReaders.DirectoryContents,
			leavesReader:            directoryReaders.Leaves,
		},
		key.Message.DirectoryLayout,
	); err != nil {
		return PatchedFilesRootValue[TMetadata]{}, err
	}

	group, groupCtx := errgroup.WithContext(ctx)
	var createdRootDirectory model_filesystem.CreatedDirectory[TMetadata]
	group.Go(func() error {
		return model_filesystem.CreateDirectoryMerkleTree[TMetadata, TMetadata](
			groupCtx,
			semaphore.NewWeighted(1),
			group,
			directoryCreationParameters,
			&capturableChangeTrackingDirectory[TReference, TMetadata]{
				options: &capturableChangeTrackingDirectoryOptions[TReference, TMetadata]{
					context:                 ctx,
					directoryContentsReader: directoryReaders.DirectoryContents,
					objectCapturer:          e,
				},
				directory: &rootDirectory,
			},
			model_filesystem.NewSimpleDirectoryMerkleTreeCapturer[TMetadata](e),
			&createdRootDirectory,
		)
	})
	if err := group.Wait(); err != nil {
		return PatchedFilesRootValue[TMetadata]{}, err
	}

	return model_core.NewPatchedMessage(
		&model_analysis_pb.FilesRoot_Value{
			RootDirectory: createdRootDirectory.Message.Message,
		},
		createdRootDirectory.Message.Patcher,
	), nil
}
