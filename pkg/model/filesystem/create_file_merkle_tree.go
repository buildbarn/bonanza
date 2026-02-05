package filesystem

import (
	"context"
	"io"
	"math"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/util"
	cdc "github.com/buildbarn/go-cdc"

	"google.golang.org/protobuf/types/known/emptypb"
)

// isAllNullBytes returns true if a byte slice only contains null bytes.
// If a file chunk only contains null bytes, it may not be stored as a
// regular file chunk. Instead, such adjoining chunks should be encoded
// as a hole.
func isAllNullBytes(chunk []byte) bool {
	for _, b := range chunk {
		if b != 0 {
			return false
		}
	}
	return true
}

// maybeWriteHole writes hole entries if one or more previous chunks
// only consisted of null bytes.
func maybeWriteHole[T model_core.ReferenceMetadata](treeBuilder btree.Builder[*model_filesystem_pb.FileContents, T], holeSizeBytes uint64) error {
	if holeSizeBytes > 0 {
		if err := treeBuilder.PushChild(
			model_core.NewSimplePatchedMessage[T](&model_filesystem_pb.FileContents{
				Level: &model_filesystem_pb.FileContents_Hole{
					Hole: &emptypb.Empty{},
				},
				TotalSizeBytes: holeSizeBytes,
			}),
		); err != nil {
			return err
		}
	}
	return nil
}

// CreateFileMerkleTree creates a Merkle tree structure that corresponds
// to the contents of a single file. If a file is small, it stores all
// of its contents in a single object. If a file is large, it creates a
// B-tree.
//
// Chunking of large files is performed using the MaxCDC algorithm. The
// resulting B-tree is a Prolly tree. This ensures that minor changes to
// a file also result in minor changes to the resulting Merkle tree.
//
// TODO: Change this function to support more efficient creation of
// Merkle trees for sparse files.
func CreateFileMerkleTree[T model_core.ReferenceMetadata](ctx context.Context, parameters *FileCreationParameters, f io.Reader, capturer FileMerkleTreeCapturer[T]) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, T], error) {
	chunker := cdc.NewRepMaxContentDefinedChunker(
		f,
		/* bufferSizeBytes = */ max(parameters.referenceFormat.GetMaximumObjectSizeBytes(), 2*parameters.chunkMinimumSizeBytes+parameters.chunkHorizonSizeBytes),
		parameters.chunkMinimumSizeBytes,
		parameters.chunkHorizonSizeBytes,
	)
	treeBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[T](
			parameters.fileContentsListMinimumSizeBytes,
			parameters.fileContentsListMaximumSizeBytes,
			/* isParent = */ func(contents *model_filesystem_pb.FileContents) bool {
				return contents.GetList() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger[*model_filesystem_pb.FileContents, T](
			parameters.fileContentsListEncoder,
			parameters.referenceFormat,
			/* parentNodeComputer = */ btree.Capturing(
				ctx,
				model_core.CreatedObjectCapturerFunc[T](capturer.CaptureFileContentsList),
				func(createdObject model_core.Decodable[model_core.MetadataEntry[T]], childNodes model_core.Message[[]*model_filesystem_pb.FileContents, object.LocalReference]) model_core.PatchedMessage[*model_filesystem_pb.FileContents, T] {
					// Compute the total file size to store
					// in the parent FileContents node.
					var totalSizeBytes uint64
					sparse := false
					for _, childNode := range childNodes.Message {
						totalSizeBytes += childNode.TotalSizeBytes
						switch childLevel := childNode.Level.(type) {
						case *model_filesystem_pb.FileContents_List_:
							sparse = sparse || childLevel.List.Sparse
						case *model_filesystem_pb.FileContents_Hole:
							sparse = true
						}
					}

					return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[T]) *model_filesystem_pb.FileContents {
						return &model_filesystem_pb.FileContents{
							Level: &model_filesystem_pb.FileContents_List_{
								List: &model_filesystem_pb.FileContents_List{
									Reference: patcher.AddDecodableReference(createdObject),
									Sparse:    sparse,
								},
							},
							TotalSizeBytes: totalSizeBytes,
						}
					})
				},
			),
		),
	)

	currentHoleSizeBytes := uint64(0)
	for {
		// Permit cancelation.
		if err := util.StatusFromContext(ctx); err != nil {
			return model_core.PatchedMessage[*model_filesystem_pb.FileContents, T]{}, err
		}

		// Read the next chunk of data from the file and create
		// a chunk object out of it.
		chunk, err := chunker.ReadNextChunk()
		if err != nil {
			if err == io.EOF {
				// Emit the final lists of FileContents
				// messages and return the FileContents
				// message of the file's root.
				if err := maybeWriteHole(treeBuilder, currentHoleSizeBytes); err != nil {
					return model_core.PatchedMessage[*model_filesystem_pb.FileContents, T]{}, err
				}
				return treeBuilder.FinalizeSingle()
			}
			return model_core.PatchedMessage[*model_filesystem_pb.FileContents, T]{}, err
		}
		if isAllNullBytes(chunk) {
			currentHoleSizeBytes += uint64(len(chunk))
		} else {
			if err := maybeWriteHole(treeBuilder, currentHoleSizeBytes); err != nil {
				return model_core.PatchedMessage[*model_filesystem_pb.FileContents, T]{}, err
			}
			currentHoleSizeBytes = 0

			encodedChunk, err := parameters.EncodeChunk(chunk)
			if err != nil {
				return model_core.PatchedMessage[*model_filesystem_pb.FileContents, T]{}, err
			}
			chunkMetadata, err := capturer.CaptureChunk(ctx, encodedChunk.Value)
			if err != nil {
				return model_core.PatchedMessage[*model_filesystem_pb.FileContents, T]{}, err
			}

			// Insert a FileContents message for it into the B-tree.
			if err := treeBuilder.PushChild(
				model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[T]) *model_filesystem_pb.FileContents {
					return &model_filesystem_pb.FileContents{
						Level: &model_filesystem_pb.FileContents_ChunkReference{
							ChunkReference: &model_core_pb.DecodableReference{
								Reference: patcher.AddReference(model_core.MetadataEntry[T]{
									LocalReference: encodedChunk.Value.GetLocalReference(),
									Metadata:       chunkMetadata,
								}),
								DecodingParameters: encodedChunk.GetDecodingParameters(),
							},
						},
						TotalSizeBytes: uint64(len(chunk)),
					}
				}),
			); err != nil {
				return model_core.PatchedMessage[*model_filesystem_pb.FileContents, T]{}, err
			}
		}
	}
}

// CreateChunkDiscardingFileMerkleTree is a helper function for creating
// a Merkle tree of a file and immediately constructing an
// ObjectContentsWalker for it. This function takes ownership of the
// file that is provided. Its contents may be re-read when the
// ObjectContentsWalker is accessed, and it will be released when no
// more ObjectContentsWalkers for the file exist.
func CreateChunkDiscardingFileMerkleTree(ctx context.Context, parameters *FileCreationParameters, f filesystem.FileReader) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker], error) {
	fileContents, err := CreateFileMerkleTree(
		ctx,
		parameters,
		io.NewSectionReader(f, 0, math.MaxInt64),
		ChunkDiscardingFileMerkleTreeCapturer,
	)
	if err != nil {
		f.Close()
		return model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker]{}, err
	}

	if fileContents.Message == nil {
		// File is empty. Close the file immediately, so that it
		// doesn't leak.
		f.Close()
		return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker]((*model_filesystem_pb.FileContents)(nil)), nil
	}

	var decodingParameters []byte
	switch level := fileContents.Message.Level.(type) {
	case *model_filesystem_pb.FileContents_List_:
		decodingParameters = level.List.Reference.DecodingParameters
	case *model_filesystem_pb.FileContents_ChunkReference:
		decodingParameters = level.ChunkReference.DecodingParameters
	case *model_filesystem_pb.FileContents_Hole:
		return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](fileContents.Message), nil
	default:
		panic("unknown file contents level")
	}

	return model_core.NewPatchedMessage(
		fileContents.Message,
		model_core.MapReferenceMessagePatcherMetadata(
			fileContents.Patcher,
			func(capturedObject model_core.MetadataEntry[model_core.CreatedObjectTree]) dag.ObjectContentsWalker {
				return NewCapturedFileWalker(
					parameters,
					f,
					capturedObject.LocalReference,
					fileContents.Message.TotalSizeBytes,
					&capturedObject.Metadata,
					decodingParameters,
				)
			},
		),
	), nil
}
