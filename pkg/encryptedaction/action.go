package encryptedaction

import (
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ActionGetPlaintext decryptes the ciphertext that is embedded in an
// action, returning it in plaintext form.
func ActionGetPlaintext(action *encryptedaction_pb.Action, sharedSecret []byte) ([]byte, error) {
	actionAEAD, err := newAEAD(sharedSecret, aeadPurposeAction)
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to create AEAD")
	}
	nonce := action.Nonce
	if actualNonceSize, expectedNonceSize := len(nonce), actionAEAD.NonceSize(); actualNonceSize != expectedNonceSize {
		return nil, status.Errorf(codes.InvalidArgument, "Nonce was %d bytes in size, while %d bytes were expected", actualNonceSize, expectedNonceSize)
	}
	additionalData := action.AdditionalData
	if additionalData == nil {
		return nil, status.Error(codes.InvalidArgument, "Action does not contain additional data")
	}
	marshaledAdditionalData, err := proto.Marshal(additionalData)
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to marshal additional data")
	}
	return actionAEAD.Open(nil, nonce, action.Ciphertext, marshaledAdditionalData)
}

// ActionSetCiphertext sets the ciphertext and nonce fields that are
// embedded in an action to correspond to a given plaintext value.
func ActionSetCiphertext(action *encryptedaction_pb.Action, sharedSecret, plaintext []byte) error {
	actionAEAD, err := newAEAD(sharedSecret, aeadPurposeAction)
	if err != nil {
		return util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to create AEAD")
	}
	marshaledActionAdditionalData, err := proto.Marshal(action.AdditionalData)
	if err != nil {
		return util.StatusWrapWithCode(err, codes.Internal, "Failed to marshal action additional data")
	}

	// TODO: We should also permit setting a random nonce in case we
	// don't want in-flight deduplication.
	actionNonce := make([]byte, actionAEAD.NonceSize())

	action.Ciphertext = actionAEAD.Seal(nil, actionNonce, plaintext, marshaledActionAdditionalData)
	action.Nonce = actionNonce
	return nil
}
