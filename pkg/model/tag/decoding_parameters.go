package tag

import (
	"crypto/sha256"
	"crypto/sha512"

	model_encoding "bonanza.build/pkg/model/encoding"
)

// GetDecodingParametersFromKeyHash converts a key hash of a tag to one
// that should actually be used at the storage level, and returns
// decoding parameters that can be used when encoding objects that
// should be referenced by the tag.
//
// Normally when encrypted objects are created, the encoding process
// yields decoding parameters that need to be stored in the encrypted
// payload of the parent object. This ensures that even when the
// encryption key is divulged, access to payloads of objects is
// restricted to ones belonging graphs to which the user has access.
//
// The issue is that the concept of decoding parameters is specific to
// the data model we build on top of the Object Store. The Object Store
// itself is oblivious of them. The same holds for the Tag Store, which
// is only capable of storing plain references to objects, which don't
// include decoding parameters.
//
// In order to decode objects referenced by tags, we let the decoding
// parameters be based on the hash in the tag's key. However, we do not
// want them to be directly derivable from the hash, as that would allow
// anyone capable of iterating tags in storage to decrypt all objects
// once the key is divulged.
//
// To solve this, we compute a SHA-512 hash of the originally computed
// tag key hash. The first half of the resulting SHA-512 hash is used as
// the tag key hash at the storage level. The other half is used as the
// decoding parameters. This means that tags can only be resolved and
// the referenced object decoded if the original tag key hash is known.
func GetDecodingParametersFromKeyHash(decoder model_encoding.BinaryDecoder, keyHash [sha256.Size]byte) ([sha256.Size]byte, []byte) {
	wrappedKeyHash := sha512.Sum512(keyHash[:])
	return *(*[sha256.Size]byte)(wrappedKeyHash[:]), wrappedKeyHash[sha256.Size:][:decoder.GetDecodingParametersSizeBytes()]
}
