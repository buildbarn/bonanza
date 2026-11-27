package format

import (
	"encoding"

	model_format_pb "bonanza.build/pkg/proto/model/format"

	"google.golang.org/protobuf/types/known/emptypb"
)

// Raw is a Format for payloads containing
// arbitrary binary data, such as file contents.
type Raw struct{}

var _ Format[[]byte] = Raw{}

// ToProto returns the Protobuf message describing the format.
func (Raw) ToProto() *model_format_pb.Format {
	return model_format_pb.Format_builder{
		Raw: &emptypb.Empty{},
	}.Build()
}

// Unmarshal returns the payload as is.
func (Raw) Unmarshal(data []byte) ([]byte, error) {
	return data, nil
}

// NewMarshaler converts a byte slice to a BinaryMarshaler that returns
// the original byte slice when marshaled.
func (Raw) NewMarshaler(value []byte) encoding.BinaryMarshaler {
	return rawBinaryMarshaler{data: value}
}

type rawBinaryMarshaler struct {
	data []byte
}

func (rm rawBinaryMarshaler) MarshalBinary() ([]byte, error) {
	return rm.data, nil
}
