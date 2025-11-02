package filesystem

import (
	"math"
	"math/bits"

	model_core "bonanza.build/pkg/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FileContentsList contains the properties of parts of a concatenated
// file. Parts are stored in the order in which they should be
// concatenated, with EndBytes increasing.
type FileContentsList[TReference object.BasicReference] []FileContentsEntry[TReference]

func NewFileContentsListFromProto[TReference object.BasicReference](l model_core.Message[[]*model_filesystem_pb.FileContents, TReference]) (FileContentsList[TReference], error) {
	if len(l.Message) < 2 {
		return nil, status.Error(codes.InvalidArgument, "File contents list contains fewer than two parts")
	}

	var endBytes uint64
	fileContentsList := make(FileContentsList[TReference], 0, len(l.Message))
	for i, part := range l.Message {
		entry, err := NewFileContentsEntryFromProto(model_core.Nested(l, part))
		if err != nil {
			return nil, util.StatusWrapf(err, "Part at index %d", i)
		}

		// Convert 'total_size_bytes' to a cumulative value, to
		// allow FileContentsIterator to perform binary searching.
		var carryOut uint64
		endBytes, carryOut = bits.Add64(endBytes, entry.endBytes, 0)
		if carryOut > 0 {
			return nil, status.Errorf(codes.InvalidArgument, "Combined size of all parts exceeds maximum file size of %d bytes", uint64(math.MaxUint64))
		}
		entry.endBytes = endBytes

		fileContentsList = append(fileContentsList, entry)
	}

	// The previous pass yielded entries where contiguousCount is
	// either set to -1 (holes), 0 (sparse lists), or 1 (non-sparse
	// lists or chunks). Fix up the positive entries, so that they
	// actually refer to the next sparse entry.
	contiguousCount := 0
	for i := len(fileContentsList) - 1; i >= 0; i-- {
		if fileContentsList[i].contiguousCount > 0 {
			contiguousCount++
			fileContentsList[i].contiguousCount = contiguousCount
		} else {
			contiguousCount = 0
		}
	}
	return fileContentsList, nil
}
