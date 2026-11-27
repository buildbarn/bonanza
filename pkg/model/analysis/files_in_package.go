package analysis

import (
	"context"
	"errors"
	"fmt"
	"iter"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

type currentPackageLimitingDirectoryOptions[TReference any] struct {
	context          context.Context
	directoryReaders *DirectoryReaders[TReference]
}

// currentPackageLimitingDirectory is an implementation of
// CapturableDirectory that suppresses child directories that are not
// part of the current package. Furthermore, it strips the contents of
// all files. This leaves the only data that is necessary for making
// features like glob() work.
type currentPackageLimitingDirectory[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	options   *currentPackageLimitingDirectoryOptions[TReference]
	directory model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]
}

func (d *currentPackageLimitingDirectory[TReference, TMetadata]) Close() error {
	*d = currentPackageLimitingDirectory[TReference, TMetadata]{}
	return nil
}

func (d *currentPackageLimitingDirectory[TReference, TMetadata]) ReadDir() ([]filesystem.FileInfo, error) {
	ctx := d.options.context
	reader := d.options.directoryReaders.DirectoryEntries

	// Iterate over the contents of subdirectories in lockstep with
	// the nodes, so that we can determine whether subdirectories
	// are packages.
	var errChildren error
	nextChild, stopChildren := iter.Pull2(model_filesystem.DirectoryAllChildren(ctx, reader, d.directory, &errChildren))
	defer stopChildren()

	// Nodes are stored in alphabetical order, meaning the results
	// don't need to be sorted.
	var fileInfos []filesystem.FileInfo
	var errNodes error
	for node := range model_filesystem.DirectoryAllNodes(ctx, reader, d.directory, &errNodes) {
		name, ok := path.NewComponent(node.Message.GetName())
		if !ok {
			return nil, fmt.Errorf("invalid name for node %#v", node.Message.GetName())
		}
		switch node.Message.WhichKind() {
		case model_filesystem_pb.DirectoryEntry_Node_Directory_case:
			// Only report directories that are not packages.
			_, childDirectory, ok := nextChild()
			if !ok {
				if errChildren != nil {
					return nil, errChildren
				}
				return nil, fmt.Errorf("contents of directory %#v are missing", node.Message.GetName())
			}
			isPackage, err := directoryIsPackage(ctx, reader, childDirectory)
			if err != nil {
				return nil, err
			}
			if !isPackage {
				fileInfos = append(fileInfos, filesystem.NewFileInfo(name, filesystem.FileTypeDirectory, false))
			}
		case model_filesystem_pb.DirectoryEntry_Node_File_case:
			fileInfos = append(fileInfos, filesystem.NewFileInfo(name, filesystem.FileTypeRegularFile, node.Message.GetFile().GetIsExecutable()))
		case model_filesystem_pb.DirectoryEntry_Node_Symlink_case:
			fileInfos = append(fileInfos, filesystem.NewFileInfo(name, filesystem.FileTypeSymlink, false))
		default:
			return nil, fmt.Errorf("node %#v has an unknown type", node.Message.GetName())
		}
	}
	if errNodes != nil {
		return nil, errNodes
	}
	return fileInfos, nil
}

func (d *currentPackageLimitingDirectory[TReference, TMetadata]) Readlink(name path.Component) (path.Parser, error) {
	node, _, err := model_filesystem.DirectoryLookupNode(d.options.context, d.options.directoryReaders.DirectoryEntries, d.directory, name)
	if err != nil {
		return nil, err
	}
	symlink := node.Message.GetSymlink()
	if symlink == nil {
		panic("attempted to read a symbolic link that was not reported by ReadDir()")
	}
	return path.UNIXFormat.NewParser(symlink.GetTarget()), nil
}

func (d *currentPackageLimitingDirectory[TReference, TMetadata]) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[TMetadata], model_filesystem.CapturableDirectory[TMetadata, TMetadata], error) {
	childDirectory, err := model_filesystem.DirectoryLookupChild(d.options.context, d.options.directoryReaders.DirectoryEntries, d.directory, name)
	if err != nil {
		return nil, nil, err
	}
	if !childDirectory.IsSet() {
		panic("attempted to enter a directory that was not reported by ReadDir()")
	}
	return nil, &currentPackageLimitingDirectory[TReference, TMetadata]{
		options:   d.options,
		directory: childDirectory,
	}, nil
}

func (currentPackageLimitingDirectory[TReference, TMetadata]) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[TMetadata], error) {
	return emptyCapturableFile[TMetadata]{}, nil
}

type emptyCapturableFile[TMetadata model_core.ReferenceMetadata] struct{}

func (emptyCapturableFile[TMetadata]) CreateFileMerkleTree(ctx context.Context) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, TMetadata], error) {
	return model_core.NewSimplePatchedMessage[TMetadata]((*model_filesystem_pb.FileContents)(nil)), nil
}
func (emptyCapturableFile[TMetadata]) Discard() {}

func (*baseComputer[TReference, TMetadata]) ComputeFilesInPackageValue(ctx context.Context, key *model_analysis_pb.FilesInPackage_Key, e FilesInPackageEnvironment[TReference, TMetadata]) (PatchedFilesInPackageValue[TMetadata], error) {
	directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObject(&model_analysis_pb.DirectoryCreationParameters_Key{})
	directoryReaders, gotDirectoryReaders := e.GetDirectoryAccessParametersObject(&model_analysis_pb.DirectoryAccessParameters_Key{})
	if !gotDirectoryCreationParameters || !gotDirectoryReaders {
		return PatchedFilesInPackageValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	packageDirectory, err := getPackageDirectory(ctx, e, directoryReaders.DirectoryEntries, key.Package)
	if err != nil {
		return PatchedFilesInPackageValue[TMetadata]{}, err
	}
	if !packageDirectory.IsSet() {
		return PatchedFilesInPackageValue[TMetadata]{}, errors.New("package directory does not exist")
	}

	var trimmedPackageDirectory model_filesystem.CreatedDirectory[TMetadata]
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		return model_filesystem.CreateDirectoryMerkleTree[TMetadata, TMetadata](
			groupCtx,
			semaphore.NewWeighted(1),
			group,
			directoryCreationParameters,
			&currentPackageLimitingDirectory[TReference, TMetadata]{
				options: &currentPackageLimitingDirectoryOptions[TReference]{
					context:          ctx,
					directoryReaders: directoryReaders,
				},
				directory: packageDirectory,
			},
			model_filesystem.NewSimpleDirectoryMerkleTreeCapturer(e),
			&trimmedPackageDirectory,
		)
	})
	if err := group.Wait(); err != nil {
		return PatchedFilesInPackageValue[TMetadata]{}, err
	}

	return model_core.NewPatchedMessage(
		&model_analysis_pb.FilesInPackage_Value{
			Directory: trimmedPackageDirectory.Message.Message,
		},
		trimmedPackageDirectory.Message.Patcher,
	), nil
}
