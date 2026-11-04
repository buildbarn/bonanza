package filesystem

import (
	"bytes"
	"context"

	model_core "bonanza.build/pkg/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateMaybeInlineFileContents creates a MaybeInlineFileContents
// message containing the provided data. If the size of the data does
// not exceed the provided limit, the data is stored inline. Otherwise
// a Merkle tree is created, in the same way as is done for files.
//
// For a given set of parameters and a given limit, this function
// yields a canonical encoding of the data. This means that two
// MaybeInlineFileContents messages are identical if and only if the
// data they contain is identical.
func CreateMaybeInlineFileContents[T model_core.ReferenceMetadata](
	ctx context.Context,
	parameters *FileCreationParameters,
	data []byte,
	maximumInlineSizeBytes int,
	capturer FileMerkleTreeCapturer[T],
) (model_core.PatchedMessage[*model_filesystem_pb.MaybeInlineFileContents, T], error) {
	if len(data) == 0 {
		return model_core.NewSimplePatchedMessage[T](&model_filesystem_pb.MaybeInlineFileContents{}), nil
	}
	if len(data) <= maximumInlineSizeBytes {
		return model_core.NewSimplePatchedMessage[T](&model_filesystem_pb.MaybeInlineFileContents{
			Level: &model_filesystem_pb.MaybeInlineFileContents_ContentsInline{
				ContentsInline: data,
			},
		}), nil
	}

	fileContents, err := CreateFileMerkleTree(ctx, parameters, bytes.NewReader(data), capturer)
	if err != nil {
		return model_core.PatchedMessage[*model_filesystem_pb.MaybeInlineFileContents, T]{}, err
	}
	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[T]) (*model_filesystem_pb.MaybeInlineFileContents, error) {
		return &model_filesystem_pb.MaybeInlineFileContents{
			Level: &model_filesystem_pb.MaybeInlineFileContents_ContentsExternal{
				ContentsExternal: fileContents.Merge(patcher),
			},
		}, nil
	})
}

// GetMaybeInlineFileContentsSizeBytes returns the size of the data
// that is stored in a MaybeInlineFileContents message, without reading
// any objects.
func GetMaybeInlineFileContentsSizeBytes(m *model_filesystem_pb.MaybeInlineFileContents) uint64 {
	switch level := m.GetLevel().(type) {
	case *model_filesystem_pb.MaybeInlineFileContents_ContentsInline:
		return uint64(len(level.ContentsInline))
	case *model_filesystem_pb.MaybeInlineFileContents_ContentsExternal:
		return level.ContentsExternal.GetTotalSizeBytes()
	default:
		return 0
	}
}

func getExternalFileContentsEntry[TReference object.BasicReference](m model_core.Message[*model_filesystem_pb.MaybeInlineFileContents, TReference]) (FileContentsEntry[TReference], error) {
	external, ok := m.Message.GetLevel().(*model_filesystem_pb.MaybeInlineFileContents_ContentsExternal)
	if !ok {
		panic("data is not stored externally")
	}
	if external.ContentsExternal == nil {
		return FileContentsEntry[TReference]{}, status.Error(codes.InvalidArgument, "External file contents not set")
	}
	return NewFileContentsEntryFromProto(model_core.Nested(m, external.ContentsExternal))
}

// MaybeInlineFileContentsReadAll returns all data that is stored in a
// MaybeInlineFileContents message.
func (fr *FileReader[TReference]) MaybeInlineFileContentsReadAll(ctx context.Context, m model_core.Message[*model_filesystem_pb.MaybeInlineFileContents, TReference], maximumSizeBytes uint64) ([]byte, error) {
	switch level := m.Message.GetLevel().(type) {
	case *model_filesystem_pb.MaybeInlineFileContents_ContentsInline:
		if sizeBytes := uint64(len(level.ContentsInline)); sizeBytes > maximumSizeBytes {
			return nil, status.Errorf(codes.InvalidArgument, "Data is %d bytes in size, which exceeds the permitted maximum of %d bytes", sizeBytes, maximumSizeBytes)
		}
		return level.ContentsInline, nil
	case *model_filesystem_pb.MaybeInlineFileContents_ContentsExternal:
		fileContents, err := getExternalFileContentsEntry(m)
		if err != nil {
			return nil, err
		}
		return fr.FileReadAll(ctx, fileContents, maximumSizeBytes)
	default:
		return nil, nil
	}
}

// MaybeInlineFileContentsReadAt reads a portion of the data that is
// stored in a MaybeInlineFileContents message. The provided offset and
// output array MUST reside within the boundaries of the data.
func (fr *FileReader[TReference]) MaybeInlineFileContentsReadAt(ctx context.Context, m model_core.Message[*model_filesystem_pb.MaybeInlineFileContents, TReference], p []byte, offsetBytes uint64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	switch level := m.Message.GetLevel().(type) {
	case *model_filesystem_pb.MaybeInlineFileContents_ContentsInline:
		if offsetBytes > uint64(len(level.ContentsInline)) {
			return 0, status.Error(codes.InvalidArgument, "Offset exceeds size of data")
		}
		n := copy(p, level.ContentsInline[offsetBytes:])
		if n != len(p) {
			return n, status.Error(codes.InvalidArgument, "Read exceeds size of data")
		}
		return n, nil
	case *model_filesystem_pb.MaybeInlineFileContents_ContentsExternal:
		fileContents, err := getExternalFileContentsEntry(m)
		if err != nil {
			return 0, err
		}
		if offsetBytes+uint64(len(p)) > fileContents.GetEndBytes() {
			return 0, status.Error(codes.InvalidArgument, "Read exceeds size of data")
		}
		return fr.FileReadAt(ctx, fileContents, p, offsetBytes)
	default:
		return 0, status.Error(codes.InvalidArgument, "Read exceeds size of data")
	}
}
