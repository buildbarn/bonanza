package filesystem

import (
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/parser"
	model_parser "bonanza.build/pkg/model/parser"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"
)

type fileContentsListObjectParser[TReference object.BasicReference] struct{}

// NewFileContentsListObjectParser creates an ObjectParser that is
// capable of parsing FileContentsList messages, turning them into a
// list of entries that can be processed by FileContentsIterator.
func NewFileContentsListObjectParser[TReference object.BasicReference]() parser.ObjectParser[TReference, FileContentsList[TReference]] {
	return &fileContentsListObjectParser[TReference]{}
}

func (fileContentsListObjectParser[TReference]) ParseObject(in model_core.Message[[]byte, TReference], decodingParameters []byte) (FileContentsList[TReference], error) {
	l, err := model_parser.NewProtoListObjectParser[TReference, model_filesystem_pb.FileContents]().
		ParseObject(in, decodingParameters)
	if err != nil {
		return nil, err
	}
	return NewFileContentsListFromProto(l)
}

func (fileContentsListObjectParser[TReference]) GetDecodingParametersSizeBytes() int {
	return 0
}
