package eval_test

import (
	"testing"

	"bonanza.build/pkg/starlark/eval"

	"github.com/stretchr/testify/require"
)

func TestPartialHash(t *testing.T) {
	thread := eval.NewThread(t.Context(), eval.Universe())
	partialHash := func(v eval.Value) uint32 {
		h, err := eval.PartialHash(thread, v)
		require.NoError(t, err)
		return h
	}
	newDict := func(entries ...eval.Value) eval.Dict {
		d := eval.NewDict()
		for i := 0; i < len(entries); i += 2 {
			require.NoError(t, d.SetKey(thread, entries[i], entries[i+1]))
		}
		return d
	}

	t.Run("Hashable", func(t *testing.T) {
		// For hashable values, the results of Hash() and
		// PartialHash() should be identical.
		for _, v := range []eval.Value{
			eval.String("hello"),
			eval.MakeInt(42),
			eval.NewTuple([]eval.Value{eval.String("a"), eval.MakeInt(1)}),
		} {
			h, err := eval.Hash(thread, v)
			require.NoError(t, err)
			require.Equal(t, h, partialHash(v))
		}
	})

	t.Run("IntFloat", func(t *testing.T) {
		// 1 == 1.0, so their hashes must be identical, even
		// when contained in unhashable values.
		require.Equal(
			t,
			partialHash(eval.NewList([]eval.Value{eval.MakeInt(1)})),
			partialHash(eval.NewList([]eval.Value{eval.Float(1)})),
		)
	})

	t.Run("DictOrder", func(t *testing.T) {
		// Dicts are equal regardless of insertion order.
		require.Equal(
			t,
			partialHash(newDict(eval.String("a"), eval.MakeInt(1), eval.String("b"), eval.MakeInt(2))),
			partialHash(newDict(eval.String("b"), eval.MakeInt(2), eval.String("a"), eval.MakeInt(1))),
		)
	})

	t.Run("HashableSiblings", func(t *testing.T) {
		// Values contained in unhashable values should still
		// contribute to the hash.
		require.NotEqual(
			t,
			partialHash(eval.NewTuple([]eval.Value{eval.String("a"), eval.NewList(nil)})),
			partialHash(eval.NewTuple([]eval.Value{eval.String("b"), eval.NewList(nil)})),
		)
		require.NotEqual(
			t,
			partialHash(newDict(eval.String("a"), eval.MakeInt(1))),
			partialHash(newDict(eval.String("a"), eval.MakeInt(2))),
		)
		require.NotEqual(
			t,
			partialHash(eval.NewList([]eval.Value{eval.MakeInt(1)})),
			partialHash(eval.NewList([]eval.Value{eval.MakeInt(2)})),
		)
	})

	t.Run("Cyclic", func(t *testing.T) {
		// Hashing cyclic values should terminate.
		l := eval.NewList(nil)
		require.NoError(t, l.Append(thread, l))
		partialHash(l)
	})
}
