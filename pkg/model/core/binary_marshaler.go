package core

import (
	"encoding"

	"bonanza.build/pkg/encoding/varint"

	"google.golang.org/protobuf/proto"
)

type protoBinaryMarshaler struct {
	message proto.Message
}

// NewProtoBinaryMarshaler converts a Protobuf message to a
// BinaryMarshaler. This prevents further access to the message's
// contents, but does allow it to be passed along in contexts where the
// data is only expected to be marshaled and stored.
func NewProtoBinaryMarshaler(message proto.Message) encoding.BinaryMarshaler {
	return protoBinaryMarshaler{
		message: message,
	}
}

var marshalOptions = proto.MarshalOptions{
	Deterministic: true,
	UseCachedSize: true,
}

func (mm protoBinaryMarshaler) MarshalBinary() ([]byte, error) {
	return marshalOptions.Marshal(mm.message)
}

type protoListBinaryMarshaler[TMessage proto.Message] struct {
	messages []TMessage
}

// NewProtoListBinaryMarshaler converts a list of Protobuf message to a
// BinaryMarshaler. This prevents further access to the message's
// contents, but does allow it to be passed along in contexts where the
// data is only expected to be marshaled and stored.
//
// When marshaled, each message is prefixed with its size.
func NewProtoListBinaryMarshaler[TMessage proto.Message](messages []TMessage) encoding.BinaryMarshaler {
	return protoListBinaryMarshaler[TMessage]{
		messages: messages,
	}
}

func (mlm protoListBinaryMarshaler[TMessage]) MarshalBinary() ([]byte, error) {
	var data []byte
	for _, message := range mlm.messages {
		data = varint.AppendForward(data, marshalOptions.Size(message))
		var err error
		data, err = marshalOptions.MarshalAppend(data, message)
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

type rawBinaryMarshaler struct {
	data []byte
}

// NewRawBinaryMarshaler converts a byte slice to a BinaryMarshaler. The
// BinaryMarshaler does nothing apart from returning the original byte
// slice. This can be used to store blobs in objects.
func NewRawBinaryMarshaler(data []byte) encoding.BinaryMarshaler {
	return rawBinaryMarshaler{
		data: data,
	}
}

func (rm rawBinaryMarshaler) MarshalBinary() ([]byte, error) {
	return rm.data, nil
}
