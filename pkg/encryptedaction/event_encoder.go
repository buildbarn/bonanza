package encryptedaction

import (
	"crypto/cipher"
	"crypto/sha256"
	"io"

	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
)

// EventEncoder can be used to convert plaintext data to encrypted Event
// messages, or vice versa.
type EventEncoder struct {
	additionalData [sha256.Size]byte
	sharedSecret   []byte
}

// NewEventEncoder creates a new EventEncoder that is capable of
// encoding or decoding events belonging to a given action.
func NewEventEncoder(action *encryptedaction_pb.Action, sharedSecret []byte) *EventEncoder {
	return &EventEncoder{
		additionalData: sha256.Sum256(action.GetCiphertext()),
		sharedSecret:   sharedSecret,
	}
}

func (ee *EventEncoder) getAEAD(isCompletionEvent bool) (cipher.AEAD, error) {
	purpose := aeadPurposeExecutionEvent
	if isCompletionEvent {
		purpose = aeadPurposeCompletionEvent
	}
	completionEventAEAD, err := newAEAD(ee.sharedSecret, purpose)
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.Internal, "Failed to create AEAD")
	}
	return completionEventAEAD, nil
}

// EncodeEvent converts plaintext data to an Event message that can be
// sent by a worker to a client.
func (ee *EventEncoder) EncodeEvent(plaintext []byte, isCompletionEvent bool, randomNumberGenerator io.Reader) (*encryptedaction_pb.Event, error) {
	aead, err := ee.getAEAD(isCompletionEvent)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(randomNumberGenerator, nonce); err != nil {
		return nil, util.StatusWrapWithCode(err, codes.Internal, "Failed to generate nonce")
	}
	return encryptedaction_pb.Event_builder{
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, plaintext, ee.additionalData[:]),
	}.Build(), nil
}

// DecodeEvent converts an Event message that was sent by a worker to a
// client back to the plaintext data contained within.
func (ee *EventEncoder) DecodeEvent(executionEvent *encryptedaction_pb.Event, isCompletionEvent bool) ([]byte, error) {
	aead, err := ee.getAEAD(isCompletionEvent)
	if err != nil {
		return nil, err
	}
	return aead.Open(
		/* dst = */ nil,
		executionEvent.GetNonce(),
		executionEvent.GetCiphertext(),
		ee.additionalData[:],
	)
}
