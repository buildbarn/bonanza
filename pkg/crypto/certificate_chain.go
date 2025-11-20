package crypto

import (
	"encoding/pem"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ParsePEMWithCertificateChain parses an X.509 certificate chain in the
// form of PEM blocks, storing each certificate in a byte slice.
func ParsePEMWithCertificateChain(data []byte) ([][]byte, error) {
	var certificates [][]byte
	for certificateBlock, remainder := pem.Decode(data); certificateBlock != nil; certificateBlock, remainder = pem.Decode(remainder) {
		if certificateBlock.Type != "CERTIFICATE" {
			return nil, status.Errorf(codes.InvalidArgument, "Certificate PEM block at index %d is not of type CERTIFICATE", len(certificates))
		}
		certificates = append(certificates, certificateBlock.Bytes)
	}
	if len(certificates) == 0 {
		return nil, status.Error(codes.InvalidArgument, "No certificate PEM blocks found")
	}
	return certificates, nil
}
