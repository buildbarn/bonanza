package tag

import (
	"crypto/ed25519"
	"crypto/sha256"
	"time"

	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Value of a tag. Tags can refer to a single root object, and they also
// keep track of a timestamp at which the tag's value was created. The
// timestamp is used to support overwriting tags reliably, as it may be
// used to determine which value is the newest.
type Value struct {
	Reference object.LocalReference
	Timestamp time.Time
}

// NewValueFromProto converts the value of a tag stored in a Protobuf
// message to a native type.
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

// Equal returns whether two tag values are equal, namely if they refer
// to the same object and were created at the same point in time.
func (v *Value) Equal(other Value) bool {
	return v.Reference == other.Reference && v.Timestamp.Equal(other.Timestamp)
}

// ToProto converts the value of a tag from the native type to a
// Protobuf message.
func (v *Value) ToProto() *tag_pb.Value {
	return &tag_pb.Value{
		Reference: v.Reference.GetRawReference(),
		Timestamp: timestamppb.New(v.Timestamp),
	}
}

func (v *Value) getSigningInput(keyHash [sha256.Size]byte) ([]byte, error) {
	valueSigningInput, err := proto.Marshal(&tag_pb.ValueSigningInput{
		ReferenceFormat: v.Reference.GetReferenceFormat().ToProto(),
		KeyHash:         keyHash[:],
		Value:           v.ToProto(),
	})
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to marshal value signing input")
	}
	return valueSigningInput, nil
}

// Sign the value of a tag. This allows consumers to validate that the
// value actually corresponds to a given key.
func (v *Value) Sign(privateKey ed25519.PrivateKey, keyHash [sha256.Size]byte) (SignedValue, error) {
	valueSigningInput, err := v.getSigningInput(keyHash)
	if err != nil {
		return SignedValue{}, err
	}
	return SignedValue{
		Value:     *v,
		Signature: *(*[ed25519.SignatureSize]byte)(ed25519.Sign(privateKey, valueSigningInput)),
	}, nil
}

// SignedValue contains the value of a tag and its signature. Instances
// of SignedValue should only be created in contexts where the signature
// of the tag has been validated.
type SignedValue struct {
	Value     Value
	Signature [ed25519.SignatureSize]byte
}

// NewSignedValueFromProto creates a signed value from a Protobuf
// message. It also validates the value's signature.
func NewSignedValueFromProto(m *tag_pb.SignedValue, referenceFormat object.ReferenceFormat, key Key) (SignedValue, error) {
	if m == nil {
		return SignedValue{}, status.Error(codes.InvalidArgument, "No signed value provided")
	}
	value, err := NewValueFromProto(m.Value, referenceFormat)
	if err != nil {
		return SignedValue{}, err
	}
	if len(m.Signature) != ed25519.SignatureSize {
		return SignedValue{}, status.Errorf(codes.InvalidArgument, "Signature is %d bytes in size, while %d bytes were expected", len(m.Signature), ed25519.SignatureSize)
	}
	return key.VerifySignature(value, (*[ed25519.SignatureSize]byte)(m.Signature))
}

// Equal returns whether two signed tag values are equal, namely if they
// refer to the same object, were created at the same time, and have the
// same signature.
func (sv *SignedValue) Equal(other SignedValue) bool {
	return sv.Value.Equal(other.Value) && sv.Signature == other.Signature
}

// ToProto converts the signed value from the native type to a Protobuf
// message.
func (sv *SignedValue) ToProto() *tag_pb.SignedValue {
	return &tag_pb.SignedValue{
		Value:     sv.Value.ToProto(),
		Signature: sv.Signature[:],
	}
}
