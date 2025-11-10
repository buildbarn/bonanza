package tag

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"

	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type Key struct {
	SignaturePublicKey [ed25519.PublicKeySize]byte
	Hash               [sha256.Size]byte
}

func NewKeyFromProto(m *tag_pb.Key) (Key, error) {
	if m == nil {
		return Key{}, status.Error(codes.InvalidArgument, "No key provided")
	}
	signaturePublicKey, err := x509.ParsePKIXPublicKey(m.SignaturePublicKey)
	if err != nil {
		return Key{}, util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid signature public key")
	}
	ed25519SignaturePublicKey, ok := signaturePublicKey.(ed25519.PublicKey)
	if !ok {
		return Key{}, status.Error(codes.InvalidArgument, "Signature public key is not of type Ed25519")
	}
	if l := len(m.Hash); l != sha256.Size {
		return Key{}, status.Errorf(codes.InvalidArgument, "Hash is %d bytes in size, while %d bytes were expected", l, sha256.Size)
	}
	return Key{
		SignaturePublicKey: *(*[ed25519.PublicKeySize]byte)(ed25519SignaturePublicKey),
		Hash:               *(*[sha256.Size]byte)(m.Hash),
	}, nil
}

func (k *Key) ToProto() (*tag_pb.Key, error) {
	signaturePublicKey, err := x509.MarshalPKIXPublicKey(ed25519.PublicKey(k.SignaturePublicKey[:]))
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.Internal, "Failed to marshal signature public key")
	}
	return &tag_pb.Key{
		SignaturePublicKey: signaturePublicKey,
		Hash:               k.Hash[:],
	}, nil
}

func (k Key) VerifySignature(value Value, signature *[ed25519.SignatureSize]byte) (SignedValue, error) {
	valueSigningInput, err := proto.Marshal(&tag_pb.ValueSigningInput{
		ReferenceFormat: value.Reference.GetReferenceFormat().ToProto(),
		KeyHash:         k.Hash[:],
		Value:           value.ToProto(),
	})
	if err != nil {
		return SignedValue{}, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to marshal value signing input")
	}
	if !ed25519.Verify(k.SignaturePublicKey[:], valueSigningInput, signature[:]) {
		return SignedValue{}, status.Error(codes.InvalidArgument, "Invalid signature")
	}
	return SignedValue{
		Value:     value,
		Signature: *signature,
	}, nil
}
