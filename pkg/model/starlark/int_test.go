package starlark_test

import (
	"math/big"
	"testing"

	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"
)

func TestInt(t *testing.T) {
	t.Run("Encoding", func(t *testing.T) {
		testutil.RequireEqualProto(t, &model_starlark_pb.Int{}, model_starlark.EncodeInt(big.NewInt(0)))
		testutil.RequireEqualProto(t, &model_starlark_pb.Int{
			AbsoluteValue: []byte{0x01, 0x00},
		}, model_starlark.EncodeInt(big.NewInt(256)))
		testutil.RequireEqualProto(t, &model_starlark_pb.Int{
			AbsoluteValue: []byte{0x2a},
			Negative:      true,
		}, model_starlark.EncodeInt(big.NewInt(-42)))
	})

	t.Run("RoundTrip", func(t *testing.T) {
		large, ok := new(big.Int).SetString("-123456789012345678901234567890", 10)
		require.True(t, ok)
		for _, i := range []*big.Int{
			big.NewInt(0),
			big.NewInt(1),
			big.NewInt(-1),
			big.NewInt(1 << 62),
			large,
		} {
			require.Equal(t, 0, i.Cmp(model_starlark.DecodeInt(model_starlark.EncodeInt(i))), i.String())
		}
	})
}
