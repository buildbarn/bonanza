package crypto

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ParsePEMWithEd25519PrivateKey parses a PEM block containing an
// Ed25519 private key.
func ParsePEMWithEd25519PrivateKey(input []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(input)
	if block == nil {
		return nil, status.Error(codes.InvalidArgument, "No PEM block found")
	}
	if block.Type != "PRIVATE KEY" {
		return nil, status.Error(codes.InvalidArgument, "PEM block is not of type PRIVATE KEY")
	}
	privateKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid private key")
	}
	ed25519PrivateKey, ok := privateKey.(ed25519.PrivateKey)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "Private key is not of type Ed25519")
	}
	return ed25519PrivateKey, nil
}
