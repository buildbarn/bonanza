package tag

import (
	"crypto/ed25519"
	"crypto/x509"

	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Key struct {
	SignaturePublicKey ed25519.PublicKey
	Hash               []byte
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
	return Key{
		SignaturePublicKey: ed25519SignaturePublicKey,
		Hash:               m.Hash,
	}, nil
}

func (k Key) ToProto() (*tag_pb.Key, error) {
	signaturePublicKey, err := x509.MarshalPKIXPublicKey(k.SignaturePublicKey)
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.Internal, "Failed to marshal signature public key")
	}
	return &tag_pb.Key{
		SignaturePublicKey: signaturePublicKey,
		Hash:               k.Hash,
	}, nil
}
