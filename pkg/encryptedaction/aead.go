package encryptedaction

import (
	"crypto/cipher"

	"github.com/ericlagergren/siv"
)

const (
	aeadPurposeAction          byte = 1
	aeadPurposeExecutionEvent  byte = 2
	aeadPurposeCompletionEvent byte = 3
)

// newAEAD created an AES-GCM-SIV AEAD that can be used to encrypt or
// decrypt actions sent by a client to a worker, or events sent by a
// worker to a client.
//
// To ensure that actions, execution events, and completion events
// cannot be interpreted in the wrong context, each use a separate key
// that is derived from the shared secret.
func newAEAD(sharedSecret []byte, purpose byte) (cipher.AEAD, error) {
	key := append([]byte(nil), sharedSecret...)
	key[0] ^= purpose
	return siv.NewGCM(key)
}
