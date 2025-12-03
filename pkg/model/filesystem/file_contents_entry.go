package filesystem

import (
	"io"

	"bonanza.build/pkg/encoding/varint"
	model_core "bonanza.build/pkg/model/core"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// FileContentsEntry contains the properties of a part of a concatenated
// file. This type is equivalent to the FileContents message, except
// that counters like size and sparseness are cumulative with respect to
// the list in which the entries are stored. This allows for efficient
// lookups.
type FileContentsEntry[TReference object.BasicReference] struct {
	// Cumulative size of the file, up to and including the current entry.
	endBytes uint64

	// When smaller than zero, the current entry is empty or
	// corresponds to a hole in the file.
	//
	// When zero, the current entry refers to a list that contains
	// one or more holes (directly or indirectly).
	//
	// When greater than zero, the next hole may be found by
	// traversing the entry stored contiguousCount places after the
	// current entry. If no holes exist in the list, contiguousCount
	// will refer to the end of the list.
	contiguousCount int

	// When contiguousCount is zero or greater, a reference to a
	// chunk or a nested FileContents list.
	reference model_core.Decodable[TReference]
}

// NewFileContentsEntryFromProto constructs a FileContentsEntry based on
// the contents of a single FileContents Protobuf message, refering to
// the file as a whole.
func NewFileContentsEntryFromProto[TReference object.BasicReference](fileContents model_core.Message[*model_filesystem_pb.FileContents, TReference]) (FileContentsEntry[TReference], error) {
	if fileContents.Message == nil {
		// File is empty, meaning that it is not backed by any
		// object. Leave the reference unset.
		return FileContentsEntry[TReference]{
			endBytes:        0,
			contiguousCount: -1,
		}, nil
	}

	if fileContents.Message.TotalSizeBytes < 1 {
		return FileContentsEntry[TReference]{}, status.Error(codes.InvalidArgument, "File contents does not have any data")
	}
	var reference model_core.Decodable[TReference]
	var contiguousCount int
	switch level := fileContents.Message.Level.(type) {
	case *model_filesystem_pb.FileContents_List_:
		var err error
		reference, err = model_core.FlattenDecodableReference(model_core.Nested(fileContents, level.List.GetReference()))
		if err != nil {
			return FileContentsEntry[TReference]{}, err
		}
		if reference.Value.GetHeight() == 0 {
			return FileContentsEntry[TReference]{}, status.Error(codes.InvalidArgument, "File contents list reference cannot have height 0")
		}
		if level.List.Sparse {
			contiguousCount = 0
		} else {
			contiguousCount = 1
		}
	case *model_filesystem_pb.FileContents_ChunkReference:
		var err error
		reference, err = model_core.FlattenDecodableReference(model_core.Nested(fileContents, level.ChunkReference))
		if err != nil {
			return FileContentsEntry[TReference]{}, err
		}
		if reference.Value.GetHeight() != 0 {
			return FileContentsEntry[TReference]{}, status.Error(codes.InvalidArgument, "Chunk reference must have height 0")
		}
		contiguousCount = 1
	case *model_filesystem_pb.FileContents_Hole:
		contiguousCount = -1
	default:
		return FileContentsEntry[TReference]{}, status.Error(codes.InvalidArgument, "Unknown file contents level")
	}
	return FileContentsEntry[TReference]{
		endBytes:        fileContents.Message.TotalSizeBytes,
		contiguousCount: contiguousCount,
		reference:       reference,
	}, nil
}

// NewFileContentsEntryFromBinary constructs a FileContentsEntry from a
// binary representation that was previously obtained by calling
// FileContentsEntry.AppendBinary().
func NewFileContentsEntryFromBinary(r io.ByteReader, getDecodingParametersSizeBytes func(isFileContentsList bool) int) (FileContentsEntry[object.LocalReference], error) {
	endBytes, err := varint.ReadForward[uint64](r)
	if err != nil {
		return FileContentsEntry[object.LocalReference]{}, err
	}
	contiguousCount := -1
	var reference model_core.Decodable[object.LocalReference]
	if endBytes > 0 {
		isNotHole, err := r.ReadByte()
		if err != nil {
			return FileContentsEntry[object.LocalReference]{}, err
		}
		if isNotHole != 0 {
			// File contents list or chunk.
			contiguousCount = 1
			referenceFormatValue, err := varint.ReadForward[object_pb.ReferenceFormat_Value](r)
			if err != nil {
				return FileContentsEntry[object.LocalReference]{}, err
			}
			referenceFormat, err := object.NewReferenceFormat(referenceFormatValue)
			if err != nil {
				return FileContentsEntry[object.LocalReference]{}, err
			}
			referenceSizeBytes := referenceFormat.GetReferenceSizeBytes()
			rawReference := make([]byte, 0, referenceSizeBytes)
			for i := 0; i < referenceSizeBytes; i++ {
				b, err := r.ReadByte()
				if err != nil {
					return FileContentsEntry[object.LocalReference]{}, err
				}
				rawReference = append(rawReference, b)
			}
			localReference, err := referenceFormat.NewLocalReference(rawReference)
			if err != nil {
				return FileContentsEntry[object.LocalReference]{}, err
			}
			isFileContentsList := localReference.GetHeight() > 0
			decodingParametersSizeBytes := getDecodingParametersSizeBytes(isFileContentsList)
			decodingParameters := make([]byte, 0, decodingParametersSizeBytes)
			for i := 0; i < int(decodingParametersSizeBytes); i++ {
				b, err := r.ReadByte()
				if err != nil {
					return FileContentsEntry[object.LocalReference]{}, err
				}
				decodingParameters = append(decodingParameters, b)
			}
			reference, err = model_core.NewDecodable(localReference, decodingParameters)
			if err != nil {
				return FileContentsEntry[object.LocalReference]{}, err
			}
			if isFileContentsList {
				sparse, err := r.ReadByte()
				if err != nil {
					return FileContentsEntry[object.LocalReference]{}, err
				}
				if sparse != 0x00 {
					contiguousCount = 0
				}
			}
		}
	}
	return FileContentsEntry[object.LocalReference]{
		endBytes:        endBytes,
		contiguousCount: contiguousCount,
		reference:       reference,
	}, nil
}

// GetEndBytes returns the byte offset at which the current entry ends,
// with respect to the file contents list in which the entry is placed.
func (fce *FileContentsEntry[TReference]) GetEndBytes() uint64 {
	return fce.endBytes
}

// GetReference returns the reference of the chunk or file contents list
// stored in this entry. If the current entry is a hole, this function
// returns nil.
func (fce *FileContentsEntry[TReference]) GetReference() *model_core.Decodable[TReference] {
	if fce.contiguousCount < 0 {
		return nil
	}
	return &fce.reference
}

// AppendBinary converts a FileContentsEntry to a compact binary
// representation. This can, for example, be embedded in the file handle
// of a file exposed via an NFSv4 mount.
func (fce *FileContentsEntry[TReference]) AppendBinary(b []byte) []byte {
	b = varint.AppendForward(b, fce.endBytes)
	if fce.endBytes > 0 {
		if fce.contiguousCount < 0 {
			// Hole.
			b = append(b, 0x00)
		} else {
			// File contents list or chunk.
			b = append(b, 0x01)
			b = varint.AppendForward(b, fce.reference.Value.GetReferenceFormat().ToProto())
			b = append(b, fce.reference.Value.GetRawReference()...)
			b = append(b, fce.reference.GetDecodingParameters()...)
			if fce.reference.Value.GetHeight() > 0 {
				// File contents list. Store whether the
				// list is sparse.
				if fce.contiguousCount == 0 {
					b = append(b, 0x01)
				} else {
					b = append(b, 0x00)
				}
			}
		}
	}
	return b
}

// FileContentsEntryToProto converts a FileContentsEntry back to a
// Protobuf message.
//
// TODO: Should this function take a model_core.ExistingObjectCapturer?
func FileContentsEntryToProto[TReference object.BasicReference](
	entry *FileContentsEntry[TReference],
) model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker] {
	if entry.endBytes == 0 {
		// Empty file is encoded as a nil message.
		return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker]((*model_filesystem_pb.FileContents)(nil))
	}

	if entry.contiguousCount < 0 {
		// Hole.
		return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](&model_filesystem_pb.FileContents{
			Level: &model_filesystem_pb.FileContents_Hole{
				Hole: &emptypb.Empty{},
			},
			TotalSizeBytes: entry.endBytes,
		})
	}

	if entry.reference.Value.GetHeight() > 0 {
		// Large file.
		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[dag.ObjectContentsWalker]) *model_filesystem_pb.FileContents {
			return &model_filesystem_pb.FileContents{
				Level: &model_filesystem_pb.FileContents_List_{
					List: &model_filesystem_pb.FileContents_List{
						Reference: &model_core_pb.DecodableReference{
							Reference: patcher.AddReference(model_core.MetadataEntry[dag.ObjectContentsWalker]{
								LocalReference: entry.reference.Value.GetLocalReference(),
								Metadata:       dag.ExistingObjectContentsWalker,
							}),
							DecodingParameters: entry.reference.GetDecodingParameters(),
						},
						Sparse: entry.contiguousCount == 0,
					},
				},
				TotalSizeBytes: entry.endBytes,
			}
		})
	}

	// Small file.
	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[dag.ObjectContentsWalker]) *model_filesystem_pb.FileContents {
		return &model_filesystem_pb.FileContents{
			Level: &model_filesystem_pb.FileContents_ChunkReference{
				ChunkReference: &model_core_pb.DecodableReference{
					Reference: patcher.AddReference(model_core.MetadataEntry[dag.ObjectContentsWalker]{
						LocalReference: entry.reference.Value.GetLocalReference(),
						Metadata:       dag.ExistingObjectContentsWalker,
					}),
					DecodingParameters: entry.reference.GetDecodingParameters(),
				},
			},
			TotalSizeBytes: entry.endBytes,
		}
	})
}
