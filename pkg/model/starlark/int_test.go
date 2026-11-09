package starlark_test

import (
	"math/big"
	"testing"

	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"
)

func TestInt(t *testing.T) {
	t.Run("Encoding", func(t *testing.T) {
		for _, tc := range []struct {
			value    int64
			expected []byte
		}{
			{0, nil},
			{1, []byte{0x01}},
			{127, []byte{0x7f}},
			{128, []byte{0x00, 0x80}},
			{255, []byte{0x00, 0xff}},
			{256, []byte{0x01, 0x00}},
			{-1, []byte{0xff}},
			{-42, []byte{0xd6}},
			{-128, []byte{0x80}},
			{-129, []byte{0xff, 0x7f}},
			{-256, []byte{0xff, 0x00}},
			{-257, []byte{0xfe, 0xff}},
		} {
			require.Equal(t, tc.expected, model_starlark.EncodeInt(big.NewInt(tc.value)), "%d", tc.value)
		}
	})

	t.Run("NonMinimalDecoding", func(t *testing.T) {
		require.Equal(t, 0, big.NewInt(5).Cmp(model_starlark.DecodeInt([]byte{0x00, 0x00, 0x05})))
		require.Equal(t, 0, big.NewInt(-1).Cmp(model_starlark.DecodeInt([]byte{0xff, 0xff, 0xff})))
	})

	t.Run("ZeroInValue", func(t *testing.T) {
		// Zero is encoded as an empty value. Its presence
		// should be retained when stored as part of a oneof.
		data, err := proto.Marshal(&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Int{Int: model_starlark.EncodeInt(big.NewInt(0))},
		})
		require.NoError(t, err)
		var value model_starlark_pb.Value
		require.NoError(t, proto.Unmarshal(data, &value))
		kind, ok := value.Kind.(*model_starlark_pb.Value_Int)
		require.True(t, ok)
		require.Equal(t, 0, model_starlark.DecodeInt(kind.Int).Sign())
	})

	t.Run("RoundTrip", func(t *testing.T) {
		large, ok := new(big.Int).SetString("-123456789012345678901234567890", 10)
		require.True(t, ok)
		for _, i := range []*big.Int{
			big.NewInt(0),
			big.NewInt(1),
			big.NewInt(-1),
			big.NewInt(127),
			big.NewInt(128),
			big.NewInt(-128),
			big.NewInt(-129),
			big.NewInt(1 << 62),
			new(big.Int).Lsh(big.NewInt(1), 64),
			new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 64)),
			large,
			new(big.Int).Neg(large),
		} {
			require.Equal(t, 0, i.Cmp(model_starlark.DecodeInt(model_starlark.EncodeInt(i))), i.String())
		}
	})
}
