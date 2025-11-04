package tag

import (
	"bytes"
	"crypto/ed25519"
	"time"

	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Value struct {
	Reference object.LocalReference
	Timestamp time.Time
}

func NewValueFromProto(m *tag_pb.Value, referenceFormat object.ReferenceFormat) (Value, error) {
	if m == nil {
		return Value{}, status.Error(codes.InvalidArgument, "No value provided")
	}

	reference, err := referenceFormat.NewLocalReference(m.Reference)
	if err != nil {
		return Value{}, util.StatusWrap(err, "Invalid reference")
	}

	timestamp := m.Timestamp
	if err := timestamp.CheckValid(); err != nil {
		return Value{}, util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid timestamp")
	}

	return Value{
		Reference: reference,
		Timestamp: timestamp.AsTime(),
	}, nil
}

func (v *Value) Equal(other Value) bool {
	return v.Reference == other.Reference && v.Timestamp.Equal(other.Timestamp)
}

func (v *Value) ToProto() *tag_pb.Value {
	return &tag_pb.Value{
		Reference: v.Reference.GetRawReference(),
		Timestamp: timestamppb.New(v.Timestamp),
	}
}

// SignedValue contains the value of a tag and its signature. Instances
// of SignedValue should only be created in contexts where the signature
// of the tag has been validated.
type SignedValue struct {
	Value     Value
	Signature []byte
}

func NewSignedValueFromProto(m *tag_pb.SignedValue, referenceFormat object.ReferenceFormat, key Key) (SignedValue, error) {
	if m == nil {
		return SignedValue{}, status.Error(codes.InvalidArgument, "No signed value provided")
	}

	value, err := NewValueFromProto(m.Value, referenceFormat)
	if err != nil {
		return SignedValue{}, err
	}

	valueSigningInput, err := proto.Marshal(&tag_pb.ValueSigningInput{
		ReferenceFormat: value.Reference.GetReferenceFormat().ToProto(),
		KeyHash:         key.Hash,
		Value:           value.ToProto(),
	})
	if err != nil {
		return SignedValue{}, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to marshal value signing input")
	}
	if len(m.Signature) != ed25519.SignatureSize {
		return SignedValue{}, status.Errorf(codes.InvalidArgument, "Signature is %d bytes in size, while %d bytes were expected", len(m.Signature), ed25519.SignatureSize)
	}
	if !ed25519.Verify(key.SignaturePublicKey[:], valueSigningInput, m.Signature) {
		return SignedValue{}, status.Error(codes.InvalidArgument, "Invalid signature")
	}

	return SignedValue{
		Value:     value,
		Signature: m.Signature,
	}, nil
}

func (sv *SignedValue) Equal(other SignedValue) bool {
	return sv.Value.Equal(other.Value) && bytes.Equal(sv.Signature, other.Signature)
}

func (sv *SignedValue) ToProto() *tag_pb.SignedValue {
	return &tag_pb.SignedValue{
		Value:     sv.Value.ToProto(),
		Signature: sv.Signature,
	}
}
