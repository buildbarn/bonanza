package encoding

import (
	"crypto/sha256"
	"unique"

	model_encoding_pb "bonanza.build/pkg/proto/model/encoding"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// BinaryDecoder can be used to decode binary data by undoing previously
// applied encoding steps. Examples of encoding steps include
// compression and encryption.
//
// For every BinaryDecoder, it is possible to get a list of unique keys
// that describe the decoding steps that are performed. This is used by
// model_parser.ParsedObjectPool, as it needs to uniquely identify
// model_parser.ObjectParsers, so that parsed objects are cached
// correctly.
type BinaryDecoder interface {
	DecodeBinary(in, parameters []byte) ([]byte, error)
	AppendUniqueDecodingKeys(keys []unique.Handle[any]) []unique.Handle[any]
	GetDecodingParametersSizeBytes() int
}

func newBinaryEncoderFromProto[T any](
	configurations []*model_encoding_pb.BinaryEncoder,
	maximumDecodedSizeBytes uint32,
	encodingMode model_encoding_pb.EncodingMode,
	newLZWCompressingBinaryEncoder func(maximumDecodedSizeBytes uint32) T,
	newEncryptingBinaryEncoder func(key, additionalData []byte) (T, error),
	newChainedBinaryEncoder func([]T) T,
) (T, error) {
	encoders := make([]T, 0, len(configurations))
	for i, configuration := range configurations {
		switch encoderConfiguration := configuration.Encoder.(type) {
		case *model_encoding_pb.BinaryEncoder_LzwCompressing:
			encoders = append(
				encoders,
				newLZWCompressingBinaryEncoder(maximumDecodedSizeBytes),
			)
		case *model_encoding_pb.BinaryEncoder_Encrypting:
			// Compute a hash of the configuration of the
			// encoders that are used in addition to
			// encryption. This has the advantage that
			// objects only pass verification if the full
			// configuration matches. This allows
			// bonanza_browser to automatically display
			// objects using the correct encoder.
			additionalDataInput, err := proto.MarshalOptions{Deterministic: true}.Marshal(
				&model_encoding_pb.EncryptingBinaryEncoderAdditionalDataInput{
					EncodingMode:       encodingMode,
					AdditionalEncoders: configurations[:i],
				},
			)
			if err != nil {
				var badBinaryEncoder T
				return badBinaryEncoder, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to marshal remaining encoders")
			}
			additionalData := sha256.Sum256(additionalDataInput)

			encoder, err := newEncryptingBinaryEncoder(encoderConfiguration.Encrypting.EncryptionKey, additionalData[:])
			if err != nil {
				var badBinaryEncoder T
				return badBinaryEncoder, err
			}
			encoders = append(encoders, encoder)
		default:
			var badBinaryEncoder T
			return badBinaryEncoder, status.Error(codes.InvalidArgument, "Unknown binary encoder type")
		}
	}
	return newChainedBinaryEncoder(encoders), nil
}

// DeterministicBinaryEncoder can be used to encode binary data.
// Examples of encoding steps include compression and encryption. These
// encoding steps must be reversible. Furthermore, they must be
// deterministic, meaning that they only depend on input data.
//
// Many applications give a special meaning to empty data (e.g., the
// default value of bytes fields in a Protobuf message being). Because
// of that, implementations of DeterministicBinaryEncoder should ensure
// that empty data should remain empty when encoded.
type DeterministicBinaryEncoder interface {
	BinaryDecoder

	EncodeBinary(in []byte) ([]byte, []byte, error)
}

// NewDeterministicBinaryEncoderFromProto creates a
// DeterministicBinaryEncoder that behaves according to the
// specification provided in the form of a Protobuf message.
func NewDeterministicBinaryEncoderFromProto(configurations []*model_encoding_pb.BinaryEncoder, maximumDecodedSizeBytes uint32) (DeterministicBinaryEncoder, error) {
	return newBinaryEncoderFromProto(
		configurations,
		maximumDecodedSizeBytes,
		model_encoding_pb.EncodingMode_DETERMINISTIC,
		NewLZWCompressingDeterministicBinaryEncoder,
		NewEncryptingDeterministicBinaryEncoder,
		NewChainedDeterministicBinaryEncoder,
	)
}

// KeyedBinaryEncoder can be used to encode binary data. It differs from
// DeterministicBinaryEncoder in that the caller of EncodeBinary() is
// free to choose the decoding parameters, as opposed to letting them be
// chosen by the implementation.
//
// KeyedBinaryEncoder can be used to encode objects that are referenced
// from tags contained in Tag Store. For these objects it is not
// possible to use DeterministicBinaryEncoder, because Tag Store
// provides no facilities for storing decoding parameters.
//
// Storing decoding parameters in Tag Store would also be undesirable,
// as that would allow objects referenced by tags to be trivially
// decrypted if an encryption key is leaked. It is better to let the
// decoding parameters be based on a part of a tag's key that is never
// written to storage.
type KeyedBinaryEncoder interface {
	BinaryDecoder

	EncodeBinary(in, parameters []byte) ([]byte, error)
}

func NewKeyedBinaryEncoderFromProto(configurations []*model_encoding_pb.BinaryEncoder, maximumDecodedSizeBytes uint32) (KeyedBinaryEncoder, error) {
	return newBinaryEncoderFromProto(
		configurations,
		maximumDecodedSizeBytes,
		model_encoding_pb.EncodingMode_KEYED,
		NewLZWCompressingKeyedBinaryEncoder,
		NewEncryptingKeyedBinaryEncoder,
		NewChainedKeyedBinaryEncoder,
	)
}
