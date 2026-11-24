package build

import (
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
)

// minimumEncryptionSeedSizeBytes is the minimum size of the value of
// --remote_encryption_seed. Keys derived from the seed are no stronger
// than the seed itself, so require it to provide at least 128 bits.
//
// HKDF does not perform any key stretching. The seed is thus expected
// to consist of random bytes, as opposed to being a passphrase. This
// is why it is base64 encoded. 32 random bytes are recommended, which
// matches the size of the derived keys.
const minimumEncryptionSeedSizeBytes = 16

// derivedKeySizeBytes is the size of keys derived from the value of
// --remote_encryption_seed. This causes AES-256 to be used.
const derivedKeySizeBytes = 32

// encryptionKeys contains the keys that are used to encrypt each kind
// of object that is created by the build. Using separate keys ensures
// that the key for one kind of object can be handed out (e.g., to
// inspect file contents) without granting access to other kinds of
// objects. It also prevents a single secret from being used for
// unrelated purposes, such as both encryption and chunking.
type encryptionKeys struct {
	directories        []byte
	fileChunks         []byte
	fileContentsLists  []byte
	chunkGearTableSeed []byte
	actions            []byte
	evaluation         []byte
	analysis           []byte
	starlarkValues     []byte
}

// deriveEncryptionKeys derives encryption keys for each kind of object
// from the value of --remote_encryption_seed, using HKDF-SHA256 (RFC
// 5869). Each key is derived using a distinct label.
//
// Changing the labels or the way keys are derived causes all keys to
// change, meaning that any previously stored objects can no longer be
// reused.
func deriveEncryptionKeys(seed []byte) (encryptionKeys, error) {
	if len(seed) < minimumEncryptionSeedSizeBytes {
		return encryptionKeys{}, fmt.Errorf("seed is %d bytes in size, while at least %d bytes are required", len(seed), minimumEncryptionSeedSizeBytes)
	}
	var err error
	derive := func(label string) []byte {
		if err != nil {
			return nil
		}
		var key []byte
		key, err = hkdf.Key(sha256.New, seed, nil, "bonanza.build remote_encryption_seed v1 "+label, derivedKeySizeBytes)
		return key
	}
	keys := encryptionKeys{
		directories:        derive("directories"),
		fileChunks:         derive("file_chunks"),
		fileContentsLists:  derive("file_contents_lists"),
		chunkGearTableSeed: derive("chunk_gear_table_seed"),
		actions:            derive("actions"),
		evaluation:         derive("evaluation"),
		analysis:           derive("analysis"),
		starlarkValues:     derive("starlark_values"),
	}
	if err != nil {
		return encryptionKeys{}, err
	}
	return keys, nil
}
