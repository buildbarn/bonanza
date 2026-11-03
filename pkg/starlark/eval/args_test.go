package eval_test

import (
	"math"
	"math/big"
	"testing"

	"bonanza.build/pkg/starlark/eval"

	"github.com/stretchr/testify/require"

	"golang.org/x/exp/constraints"
)

func testAsIntSuccess[T constraints.Integer](t *testing.T, v eval.Value, expected T) {
	t.Helper()
	actual, err := eval.AsInt[T](v)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
}

func testAsIntOutOfRange[T constraints.Integer](t *testing.T, v eval.Value) {
	t.Helper()
	_, err := eval.AsInt[T](v)
	require.ErrorContains(t, err, "out of range")
}

func TestAsInt(t *testing.T) {
	minInt64 := eval.MakeInt64(math.MinInt64)
	maxInt64 := eval.MakeInt64(math.MaxInt64)
	maxInt64Plus1 := eval.MakeBigInt(new(big.Int).Lsh(big.NewInt(1), 63))
	maxUint64 := eval.MakeBigInt(new(big.Int).SetUint64(math.MaxUint64))
	maxUint64Plus1 := eval.MakeBigInt(new(big.Int).Lsh(big.NewInt(1), 64))
	belowMinInt64 := eval.MakeBigInt(new(big.Int).Sub(big.NewInt(math.MinInt64), big.NewInt(1)))

	t.Run("Uint64", func(t *testing.T) {
		testAsIntSuccess[uint64](t, eval.MakeInt(0), 0)
		testAsIntSuccess[uint64](t, maxInt64, math.MaxInt64)
		testAsIntSuccess[uint64](t, maxInt64Plus1, 1<<63)
		testAsIntSuccess[uint64](t, maxUint64, math.MaxUint64)
		testAsIntOutOfRange[uint64](t, maxUint64Plus1)
		testAsIntOutOfRange[uint64](t, eval.MakeInt(-1))
		testAsIntOutOfRange[uint64](t, minInt64)
	})

	t.Run("Int64", func(t *testing.T) {
		testAsIntSuccess[int64](t, minInt64, math.MinInt64)
		testAsIntSuccess[int64](t, maxInt64, math.MaxInt64)
		testAsIntOutOfRange[int64](t, maxInt64Plus1)
		// This value has the same bit pattern as -1.
		testAsIntOutOfRange[int64](t, maxUint64)
		testAsIntOutOfRange[int64](t, belowMinInt64)
	})

	t.Run("Uint8", func(t *testing.T) {
		testAsIntSuccess[uint8](t, eval.MakeInt(255), 255)
		testAsIntOutOfRange[uint8](t, eval.MakeInt(256))
		testAsIntOutOfRange[uint8](t, eval.MakeInt(-1))
	})

	t.Run("Int8", func(t *testing.T) {
		testAsIntSuccess[int8](t, eval.MakeInt(-128), -128)
		testAsIntSuccess[int8](t, eval.MakeInt(127), 127)
		testAsIntOutOfRange[int8](t, eval.MakeInt(128))
		testAsIntOutOfRange[int8](t, eval.MakeInt(-129))
	})

	t.Run("NotAnInt", func(t *testing.T) {
		_, err := eval.AsInt[int](eval.String("1"))
		require.ErrorContains(t, err, "got string, want int")
	})
}
