package filesystem

import (
	"unique"

	model_core "bonanza.build/pkg/model/core"
	model_format "bonanza.build/pkg/model/format"
	"bonanza.build/pkg/model/parser"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fileContentsListObjectParser[TReference object.BasicReference] struct{}

// NewFileContentsListObjectParser creates an ObjectParser that is
// capable of parsing FileContentsList messages, turning them into a
// list of entries that can be processed by FileContentsIterator.
func NewFileContentsListObjectParser[TReference object.BasicReference]() parser.ObjectParser[TReference, FileContentsList[TReference]] {
	return &fileContentsListObjectParser[TReference]{}
}

func (fileContentsListObjectParser[TReference]) ParseObject(in model_core.Message[[]byte, TReference], decodingParameters []byte) (FileContentsList[TReference], error) {
	if len(decodingParameters) > 0 {
		return nil, status.Error(codes.InvalidArgument, "Unexpected decoding parameters")
	}
	l, err := model_format.NewProtoList[model_filesystem_pb.FileContents]().Unmarshal(in.Message)
	if err != nil {
		return nil, err
	}
	return NewFileContentsListFromProto(model_core.Nested(in, l))
}

type fileContentsListObjectParserKey struct{}

var fileContentsListObjectParserKeyHandle = unique.Make[any](fileContentsListObjectParserKey{})

func (fileContentsListObjectParser[TReference]) AppendUniqueKeys(keys []unique.Handle[any]) []unique.Handle[any] {
	return append(keys, fileContentsListObjectParserKeyHandle)
}

func (fileContentsListObjectParser[TReference]) GetDecodingParametersSizeBytes() int {
	return 0
}
