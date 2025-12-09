package tag

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
)

// DecodableKeyHash contains a key hash that can be used to look up a
// tag in Tag Store. It also contains the decoding parameters that are
// needed to decode any objects referenced by it.
type DecodableKeyHash = model_core.Decodable[[sha256.Size]byte]

var marshalOptions = proto.MarshalOptions{Deterministic: true}

// NewDecodableKeyHashFromMessage computes a key hash of a tag, using a
// Protobuf message as an input. The message is first wrapped in a
// google.protobuf.Any to ensure there are no collisions in case different
// message types are used.
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
// To solve this, we compute a SHA-512 hash of the message. The first
// half of the resulting SHA-512 hash is used as the tag key hash at the
// storage level. The other half is used as the decoding parameters.
// This means that tags can only be resolved and the referenced object
// decoded if the original tag key hash is known.
func NewDecodableKeyHashFromMessage[TMessage proto.Message, TReference object.BasicReference](
	m model_core.TopLevelMessage[TMessage, TReference],
	decodingParametersSizeBytes int,
) (DecodableKeyHash, error) {
	anyMessage, err := model_core.MarshalTopLevelAny(m)
	if err != nil {
		var badKeyHash DecodableKeyHash
		return badKeyHash, err
	}
	marshaledMessage, err := model_core.MarshalTopLevelMessage(anyMessage)
	if err != nil {
		var badKeyHash DecodableKeyHash
		return badKeyHash, err
	}
	messageHash := sha512.Sum512(marshaledMessage)
	return model_core.NewDecodable(
		*(*[sha256.Size]byte)(messageHash[:]),
		messageHash[sha256.Size:][:decodingParametersSizeBytes],
	)
}

// DecodableKeyHashToString converts a tag's key hash including its
// decoding parameters to a string representations.
//
// TODO: Maybe this function should not exist, and we should only
// provide a version that converts a model_core.Decodable[tag.Key] to a
// string? Including the signature public key in the output would be
// necessary for linking to pages like bonanza_browser.
func DecodableKeyHashToString(dkh DecodableKeyHash) string {
	decodingParameters := dkh.GetDecodingParameters()
	buf := make([]byte, 0, base64.RawURLEncoding.EncodedLen(len(dkh.Value))+1+base64.RawURLEncoding.EncodedLen(len(decodingParameters)))
	buf = base64.RawURLEncoding.AppendEncode(buf, dkh.Value[:])
	buf = append(buf, '.')
	buf = base64.RawURLEncoding.AppendEncode(buf, decodingParameters)
	return string(buf)
}
