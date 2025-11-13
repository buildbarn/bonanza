package encoding

import (
	"crypto/sha256"

	model_encoding_pb "bonanza.build/pkg/proto/model/encoding"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/secure-io/siv-go"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// BinaryDecoder can be used to decode binary data by undoing previously
// applied encoding steps. Examples of encoding steps include
// compression and encryption.
type BinaryDecoder interface {
	DecodeBinary(in, parameters []byte) ([]byte, error)
	GetDecodingParametersSizeBytes() int
}

// BinaryEncoder can be used to encode binary data. Examples of encoding
// steps include compression and encryption. These encoding steps must
// be reversible.
//
// Many applications give a special meaning to empty data (e.g., the
// default value of bytes fields in a Protobuf message being). Because
// of that, implementations of BinaryEncoder should ensure that empty
// data should remain empty when encoded.
type BinaryEncoder interface {
	BinaryDecoder

	EncodeBinary(in []byte) ([]byte, []byte, error)
}

// NewBinaryEncoderFromProto creates a BinaryEncoder that behaves
// according to the specification provided in the form of a Protobuf
// message.
func NewBinaryEncoderFromProto(configurations []*model_encoding_pb.BinaryEncoder, maximumDecodedSizeBytes uint32) (BinaryEncoder, error) {
	encoders := make([]BinaryEncoder, 0, len(configurations))
	for i, configuration := range configurations {
		switch encoderConfiguration := configuration.Encoder.(type) {
		case *model_encoding_pb.BinaryEncoder_LzwCompressing:
			encoders = append(
				encoders,
				NewLZWCompressingBinaryEncoder(maximumDecodedSizeBytes),
			)
		case *model_encoding_pb.BinaryEncoder_Encrypting:
			aead, err := siv.NewGCM(encoderConfiguration.Encrypting.EncryptionKey)
			if err != nil {
				return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid encryption key")
			}

			// Compute a hash of the configuration of the
			// encoders that are used in addition to
			// encryption. This has the advantage that
			// objects only pass verification if the full
			// configuration matches. This allows
			// bonanza_browser to automatically display
			// objects using the correct encoder.
			remainingEncoders, err := proto.MarshalOptions{Deterministic: true}.Marshal(
				&model_encoding_pb.BinaryEncoderList{
					Encoders: configurations[:i],
				},
			)
			if err != nil {
				return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to marshal remaining encoders")
			}
			additionalData := sha256.Sum256(remainingEncoders)

			encoders = append(
				encoders,
				NewEncryptingBinaryEncoder(aead, additionalData[:]),
			)
		default:
			return nil, status.Error(codes.InvalidArgument, "Unknown binary encoder type")
		}
	}
	return NewChainedBinaryEncoder(encoders), nil
}
