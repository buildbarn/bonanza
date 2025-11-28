package tag

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"

	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Key of tags stored in Tag Store.
//
// Keys contain a hash. The method for computing the hash is specific to
// the use case. Even though the protocol does not place restrictions on
// the size of the hash, this implementation only supports hashes that
// are 256 bits in size (e.g., SHA-256).
//
// In order to prevent collisions between different use cases, keys also
// contain an signature public key. The value of the tag needs to be
// signed using the corresponding private key. This permits consumers of
// tags to verify the tag's value. Even though the protocol does not
// place restrictions on which algorithm is used, this implementation
// requires keys to be of type Ed25519.
type Key struct {
	SignaturePublicKey [ed25519.PublicKeySize]byte
	Hash               [sha256.Size]byte
}

// NewKeyFromProto creates a tag key based on its equivalent Protobuf
// message.
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

// ToProto converts a tag key to an equivalent Protobuf message.
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

// VerifySignature verifies that a signature corresponds to a given tag
// key and value. Upon success, it upgrades the provided value to a
// signed value.
func (k Key) VerifySignature(value Value, signature *[ed25519.SignatureSize]byte) (SignedValue, error) {
	valueSigningInput, err := value.getSigningInput(k.Hash)
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
