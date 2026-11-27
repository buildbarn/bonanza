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
)

type addFilesToMergedDirectoryEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ExistingObjectCapturer[TReference, TMetadata]

	FilesRootValueGetter[TReference, TMetadata]
	FileRootValueGetter[TReference, TMetadata]
}

// addFilesToMergedDirectory merges the root directories of all files
// contained in a list into a MergedDirectory.
func addFilesToMergedDirectory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e addFilesToMergedDirectoryEnvironment[TReference, TMetadata],
	files model_core.Message[[]*model_starlark_pb.List_Element, TReference],
	out *model_filesystem.MergedDirectory[TReference],
	directoryLayout model_analysis_pb.DirectoryLayout,
) error {
	missingDependencies := false
	for i, element := range files.Message {
		switch element.WhichLevel() {
		case model_starlark_pb.List_Element_Parent_case:
			patchedReference := model_core.Patch(e, model_core.Nested(files, element.GetParent().GetReference()))
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
			out.MergeDirectoryContents(model_core.Nested(v, v.Message.GetRootDirectory()))
		case model_starlark_pb.List_Element_Leaf_case:
			file := element.GetLeaf().GetFile()
			if file == nil {
				return fmt.Errorf("element at index %d is not a file", i)
			}
			if err := addFileToMergedDirectory(e, model_core.Nested(files, file), out, directoryLayout); err != nil {
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

// addFileToMergedDirectory merges the root directory of a single file
// into a MergedDirectory.
func addFileToMergedDirectory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e addFilesToMergedDirectoryEnvironment[TReference, TMetadata],
	file model_core.Message[*model_starlark_pb.File, TReference],
	out *model_filesystem.MergedDirectory[TReference],
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
	out.MergeDirectoryContents(model_core.Nested(v, v.Message.RootDirectory))
	return nil
}

func (*baseComputer[TReference, TMetadata]) ComputeFilesRootValue(ctx context.Context, key model_core.Message[*model_analysis_pb.FilesRoot_Key, TReference], e FilesRootEnvironment[TReference, TMetadata]) (PatchedFilesRootValue[TMetadata], error) {
	valueReaders, gotValueReaders := getStarlarkValueReaders(e)

	directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObject(&model_analysis_pb.DirectoryCreationParameters_Key{})
	directoryReaders, gotDirectoryReaders := e.GetDirectoryAccessParametersObject(&model_analysis_pb.DirectoryAccessParameters_Key{})
	if !gotDirectoryCreationParameters || !gotDirectoryReaders || !gotValueReaders {
		return PatchedFilesRootValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	files, err := model_parser.Dereference(ctx, valueReaders.List, model_core.Nested(key, key.Message.ListReference))
	if err != nil {
		return PatchedFilesRootValue[TMetadata]{}, err
	}

	var rootDirectory model_filesystem.MergedDirectory[TReference]
	if err := addFilesToMergedDirectory(e, files, &rootDirectory, key.Message.DirectoryLayout); err != nil {
		return PatchedFilesRootValue[TMetadata]{}, err
	}

	createdRootDirectory, err := model_filesystem.CreateMergedDirectory(
		ctx,
		directoryReaders.DirectoryEntries,
		directoryCreationParameters,
		e,
		model_filesystem.MergeConflictPolicyError,
		&rootDirectory,
	)
	if err != nil {
		return PatchedFilesRootValue[TMetadata]{}, err
	}

	return model_core.NewPatchedMessage(
		&model_analysis_pb.FilesRoot_Value{
			RootDirectory: createdRootDirectory.Message.Message,
		},
		createdRootDirectory.Message.Patcher,
	), nil
}
