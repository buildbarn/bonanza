package format

import (
	"encoding"

	model_format_pb "bonanza.build/pkg/proto/model/format"
)

// Marshaler converts values of a given type to a payload, such as the
// contents of an object or the value of a bonanza.model.core.Opaque.
type Marshaler[T any] interface {
	// NewMarshaler converts a native value to a BinaryMarshaler.
	// This prevents further access to the value's contents, but
	// does allow it to be passed along in contexts where the data
	// is only expected to be marshaled and stored.
	NewMarshaler(value T) encoding.BinaryMarshaler
}

// Unmarshaler converts payloads back to values of a given type.
type Unmarshaler[T any] interface {
	// Unmarshal a payload to its native representation.
	Unmarshal(data []byte) (T, error)
}

// Format is capable of both marshaling and unmarshaling values of a
// given type. In addition to that, it can describe the format of the
// payload in the form of a Protobuf message.
type Format[T any] interface {
	Marshaler[T]
	Unmarshaler[T]

	// ToProto returns the Protobuf message describing the format,
	// which can be attached to references to make objects
	// browsable.
	ToProto() *model_format_pb.Format
}

// ToPath converts a format to a set of pathname
// components. This corresponds to the paths accepted by tools like
// bonanza_browser.
func ToPath(objectFormat *model_format_pb.Format) ([]string, bool) {
	switch format := objectFormat.GetFormat().(type) {
	case *model_format_pb.Format_Raw:
		return []string{"raw"}, true
	case *model_format_pb.Format_ProtoTypeName:
		return []string{"proto", format.ProtoTypeName}, true
	case *model_format_pb.Format_ProtoListTypeName:
		return []string{"proto_list", format.ProtoListTypeName}, true
	default:
		return nil, false
	}
}
