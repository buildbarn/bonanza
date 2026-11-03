package starlark_test

import (
	"context"
	"fmt"
	"math/big"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark "bonanza.build/pkg/model/starlark"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"go.starlark.net/starlark"
)

// TestEncodeEvalValue checks that values of pkg/starlark/eval are
// encoded identically to the equivalent values of go.starlark.net.
func TestEncodeEvalValue(t *testing.T) {
	options := &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.NoopReferenceMetadata]{
		Context:                context.Background(),
		ObjectEncoder:          model_encoding.NewChainedDeterministicBinaryEncoder(nil),
		ObjectReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
		ObjectCapturer:         model_core.NewDiscardingObjectCapturer[object.LocalReference](),
		ObjectMinimumSizeBytes: 1024,
		ObjectMaximumSizeBytes: 4096,
	}
	legacyThread := &starlark.Thread{}

	// Build the same value using both implementations. The list
	// is large enough to be split into multiple objects.
	bigInt, ok := new(big.Int).SetString("123456789012345678901234567890", 10)
	require.True(t, ok)

	var evalElements []eval.Value
	var legacyListElements []starlark.Value
	for i := 0; i < 1000; i++ {
		evalElements = append(evalElements, eval.String(fmt.Sprintf("element %d", i)))
		legacyListElements = append(legacyListElements, starlark.String(fmt.Sprintf("element %d", i)))
	}

	evalDict := eval.NewDict()
	require.NoError(t, evalDict.SetKey(nil, eval.String("list"), eval.NewList(evalElements)))
	require.NoError(t, evalDict.SetKey(nil, eval.String("tuple"), eval.NewTuple([]eval.Value{eval.None, eval.True, eval.MakeInt(-42), eval.MakeBigInt(bigInt)})))
	evalSet := eval.NewSet()
	require.NoError(t, evalSet.Add(nil, eval.MakeInt(3)))
	require.NoError(t, evalSet.Add(nil, eval.MakeInt(1)))
	require.NoError(t, evalDict.SetKey(nil, eval.String("set"), evalSet))
	require.NoError(t, evalDict.SetKey(nil, eval.MakeInt(7), eval.String("int key")))

	legacyDict := starlark.NewDict(0)
	require.NoError(t, legacyDict.SetKey(legacyThread, starlark.String("list"), starlark.NewList(legacyListElements)))
	require.NoError(t, legacyDict.SetKey(legacyThread, starlark.String("tuple"), starlark.Tuple{starlark.None, starlark.True, starlark.MakeInt(-42), starlark.MakeBigInt(bigInt)}))
	legacySet := starlark.NewSet(0)
	require.NoError(t, legacySet.Insert(legacyThread, starlark.MakeInt(3)))
	require.NoError(t, legacySet.Insert(legacyThread, starlark.MakeInt(1)))
	require.NoError(t, legacyDict.SetKey(legacyThread, starlark.String("set"), legacySet))
	require.NoError(t, legacyDict.SetKey(legacyThread, starlark.MakeInt(7), starlark.String("int key")))

	t.Run("Value", func(t *testing.T) {
		got, err := model_starlark.EncodeEvalValue(nil, evalDict, options)
		require.NoError(t, err)
		want, _, err := model_starlark.EncodeValue[object.LocalReference, model_core.NoopReferenceMetadata](legacyDict, map[starlark.Value]struct{}{}, nil, options)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, want.Message, got.Message)
	})

	t.Run("StructFields", func(t *testing.T) {
		got, err := model_starlark.EncodeEvalStructFields(nil, map[string]eval.Value{
			"b": eval.NewList(evalElements),
			"a": eval.String("hello"),
		}, options)
		require.NoError(t, err)
		want, _, err := model_starlark.NewStructFromDict[object.LocalReference, model_core.NoopReferenceMetadata](nil, map[string]any{
			"b": starlark.NewList(legacyListElements),
			"a": starlark.String("hello"),
		}).EncodeStructFields(map[starlark.Value]struct{}{}, options)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, want.Message, got.Message)
	})

	t.Run("Cycle", func(t *testing.T) {
		l := eval.NewList(nil)
		require.NoError(t, l.Append(nil, l))
		_, err := model_starlark.EncodeEvalValue(nil, l, options)
		require.ErrorContains(t, err, "value is defined recursively")
	})

	t.Run("Unsupported", func(t *testing.T) {
		_, err := model_starlark.EncodeEvalValue(nil, eval.Float(1.5), options)
		require.ErrorContains(t, err, "value of type float cannot be encoded")
	})
}
