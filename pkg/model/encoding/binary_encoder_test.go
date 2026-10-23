package encoding_test

import (
	"bytes"
	"testing"

	model_encoding "bonanza.build/pkg/model/encoding"
	model_encoding_pb "bonanza.build/pkg/proto/model/encoding"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestBinaryEncoderFactory(t *testing.T) {
	lzwCompressing := &model_encoding_pb.BinaryEncoder{
		Encoder: &model_encoding_pb.BinaryEncoder_LzwCompressing{
			LzwCompressing: &emptypb.Empty{},
		},
	}
	encrypting := &model_encoding_pb.BinaryEncoder{
		Encoder: &model_encoding_pb.BinaryEncoder_Encrypting{
			Encrypting: &model_encoding_pb.EncryptingBinaryEncoder{
				EncryptionKey: bytes.Repeat([]byte{0x42}, 32),
			},
		},
	}
	errUnencrypted := status.Error(codes.InvalidArgument, "Binary encoders do not perform encryption, which is required by this server")

	for _, tc := range []struct {
		name           string
		configurations []*model_encoding_pb.BinaryEncoder
		isEncrypting   bool
	}{
		{name: "Empty", configurations: nil, isEncrypting: false},
		{name: "LZWCompressing", configurations: []*model_encoding_pb.BinaryEncoder{lzwCompressing}, isEncrypting: false},
		{name: "Encrypting", configurations: []*model_encoding_pb.BinaryEncoder{encrypting}, isEncrypting: true},
		// Compressing prior to encrypting is the typical setup.
		{name: "LZWCompressingAndEncrypting", configurations: []*model_encoding_pb.BinaryEncoder{lzwCompressing, encrypting}, isEncrypting: true},
		// Steps applied after encrypting only see ciphertext,
		// meaning the data that is stored remains encrypted.
		{name: "EncryptingAndLZWCompressing", configurations: []*model_encoding_pb.BinaryEncoder{encrypting, lzwCompressing}, isEncrypting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("Strict", func(t *testing.T) {
				// The zero value of BinaryEncoderFactory
				// only accepts configurations that
				// perform encryption.
				var binaryEncoderFactory model_encoding.BinaryEncoderFactory

				_, err := binaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(tc.configurations, 1<<20)
				if tc.isEncrypting {
					require.NoError(t, err)
				} else {
					testutil.RequireEqualStatus(t, errUnencrypted, err)
				}

				_, err = binaryEncoderFactory.NewKeyedBinaryEncoderFromProto(tc.configurations, 1<<20)
				if tc.isEncrypting {
					require.NoError(t, err)
				} else {
					testutil.RequireEqualStatus(t, errUnencrypted, err)
				}
			})

			t.Run("Permissive", func(t *testing.T) {
				binaryEncoderFactory := model_encoding.NewBinaryEncoderFactory(true)

				_, err := binaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(tc.configurations, 1<<20)
				require.NoError(t, err)

				_, err = binaryEncoderFactory.NewKeyedBinaryEncoderFromProto(tc.configurations, 1<<20)
				require.NoError(t, err)
			})
		})
	}

	t.Run("ExplicitlyStrict", func(t *testing.T) {
		// Passing false to NewBinaryEncoderFactory() should be
		// equivalent to using the zero value.
		_, err := model_encoding.NewBinaryEncoderFactory(false).NewDeterministicBinaryEncoderFromProto(
			[]*model_encoding_pb.BinaryEncoder{lzwCompressing},
			1<<20,
		)
		testutil.RequireEqualStatus(t, errUnencrypted, err)
	})
}
