package main

import (
	"bytes"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	browser_pb "bonanza.build/pkg/proto/browser"
	model_encoding_pb "bonanza.build/pkg/proto/model/encoding"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"
)

func newEncryptingConfiguration(key byte) []*model_encoding_pb.BinaryEncoder {
	return []*model_encoding_pb.BinaryEncoder{model_encoding_pb.BinaryEncoder_builder{
		Encrypting: model_encoding_pb.EncryptingBinaryEncoder_builder{
			EncryptionKey: bytes.Repeat([]byte{key}, 32),
		}.Build(),
	}.Build()}
}

func newRecentlyObservedEncoders(configurations ...[]*model_encoding_pb.BinaryEncoder) []*browser_pb.RecentlyObservedEncoder {
	recentlyObservedEncoders := make([]*browser_pb.RecentlyObservedEncoder, 0, len(configurations))
	for _, configuration := range configurations {
		recentlyObservedEncoders = append(recentlyObservedEncoders, browser_pb.RecentlyObservedEncoder_builder{
			Configuration: configuration,
		}.Build())
	}
	return recentlyObservedEncoders
}

func TestDecodeObject(t *testing.T) {
	// Create an object that is encrypted using a given key.
	binaryEncoder, err := browserBinaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(newEncryptingConfiguration(1), 1<<20)
	require.NoError(t, err)
	encodedPayload, decodingParameters, err := binaryEncoder.EncodeBinary([]byte("Hello"))
	require.NoError(t, err)
	encryptedObject, err := model_core.NewDecodable(
		object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, nil, encodedPayload),
		decodingParameters,
	)
	require.NoError(t, err)

	// Create an object that is not encoded at all.
	plainObject, err := model_core.NewDecodable(
		object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, nil, []byte("Hello")),
		nil,
	)
	require.NoError(t, err)

	t.Run("FirstEncoder", func(t *testing.T) {
		decodedPayload, usedEncoderIndex, err := decodeObject(encryptedObject, newRecentlyObservedEncoders(
			newEncryptingConfiguration(1),
			newEncryptingConfiguration(2),
		))
		require.NoError(t, err)
		require.Equal(t, []byte("Hello"), decodedPayload)
		require.Equal(t, 0, usedEncoderIndex)
	})

	t.Run("FallbackToAutoDetectingEncoder", func(t *testing.T) {
		// If the first encoder fails, auto-detecting encoders
		// should be tried.
		decodedPayload, usedEncoderIndex, err := decodeObject(encryptedObject, newRecentlyObservedEncoders(
			nil,
			newEncryptingConfiguration(2),
			newEncryptingConfiguration(1),
		))
		require.NoError(t, err)
		require.Equal(t, []byte("Hello"), decodedPayload)
		require.Equal(t, 2, usedEncoderIndex)
	})

	t.Run("SkipInvalidEncoders", func(t *testing.T) {
		// Encoders that cannot be constructed (e.g., because
		// the encryption key has an unsupported size) should
		// not prevent other encoders from being tried.
		invalidConfiguration := []*model_encoding_pb.BinaryEncoder{model_encoding_pb.BinaryEncoder_builder{
			Encrypting: model_encoding_pb.EncryptingBinaryEncoder_builder{
				EncryptionKey: []byte("short"),
			}.Build(),
		}.Build()}
		require.Error(t, validateEncoderConfiguration(invalidConfiguration))

		decodedPayload, usedEncoderIndex, err := decodeObject(encryptedObject, newRecentlyObservedEncoders(
			invalidConfiguration,
			newEncryptingConfiguration(1),
		))
		require.NoError(t, err)
		require.Equal(t, []byte("Hello"), decodedPayload)
		require.Equal(t, 1, usedEncoderIndex)
	})

	t.Run("NoFallbackToOtherEncoders", func(t *testing.T) {
		// Encoders that are not auto-detecting should not be
		// tried, unless they are the first encoder. The empty
		// configuration would otherwise successfully "decode"
		// any object without decoding parameters.
		_, _, err := decodeObject(plainObject, newRecentlyObservedEncoders(
			newEncryptingConfiguration(1),
			nil,
		))
		require.EqualError(t, err, "object can't be decoded using the selected encoder or any of the recently observed auto-detecting encoders")

		decodedPayload, usedEncoderIndex, err := decodeObject(plainObject, newRecentlyObservedEncoders(
			nil,
			newEncryptingConfiguration(1),
		))
		require.NoError(t, err)
		require.Equal(t, []byte("Hello"), decodedPayload)
		require.Equal(t, 0, usedEncoderIndex)
	})
}
