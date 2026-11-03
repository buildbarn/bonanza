package starlark_test

import (
	"context"
	"fmt"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// inMemoryObjectStore captures objects that are created while
// encoding values, and allows them to be read back.
type inMemoryObjectStore struct {
	objects map[object.LocalReference]*object.Contents
}

func (s *inMemoryObjectStore) CaptureCreatedObject(ctx context.Context, createdObject model_core.CreatedObject[model_core.NoopReferenceMetadata]) (model_core.NoopReferenceMetadata, error) {
	s.objects[createdObject.Contents.GetLocalReference()] = createdObject.Contents
	return model_core.NoopReferenceMetadata{}, nil
}

func (*inMemoryObjectStore) CaptureExistingObject(object.LocalReference) model_core.NoopReferenceMetadata {
	return model_core.NoopReferenceMetadata{}
}

func (s *inMemoryObjectStore) ReadObject(ctx context.Context, reference object.LocalReference) (model_core.Message[[]byte, object.LocalReference], error) {
	contents, ok := s.objects[reference]
	if !ok {
		return model_core.Message[[]byte, object.LocalReference]{}, status.Error(codes.NotFound, "Object not found")
	}
	return model_core.NewRawMessage(contents), nil
}

func newProtoListReader[TMessage any, TMessagePtr interface {
	*TMessage
	proto.Message
}](store *inMemoryObjectStore, encoder model_encoding.DeterministicBinaryEncoder) model_parser.MessageObjectReader[object.LocalReference, []TMessagePtr] {
	return model_parser.NewParsedObjectReader(
		store,
		model_parser.NewChainedObjectParser(
			model_parser.NewEncodedObjectParser[object.LocalReference](encoder),
			model_parser.NewProtoListObjectParser[object.LocalReference, TMessage, TMessagePtr](),
		),
	)
}

func TestDecodeValue(t *testing.T) {
	ctx := t.Context()
	encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
	store := &inMemoryObjectStore{objects: map[object.LocalReference]*object.Contents{}}
	encodingOptions := &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.NoopReferenceMetadata]{
		Context:                ctx,
		ObjectEncoder:          encoder,
		ObjectReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
		ObjectCapturer:         store,
		ObjectMinimumSizeBytes: 1024,
		ObjectMaximumSizeBytes: 4096,
	}
	decodingOptions := &model_starlark.ValueDecodingOptions[object.LocalReference]{
		Readers: &model_starlark.ValueReaders[object.LocalReference]{
			Dict: newProtoListReader[model_starlark_pb.Dict_Entry](store, encoder),
			List: newProtoListReader[model_starlark_pb.List_Element](store, encoder),
		},
	}
	thread := eval.NewThread(ctx, eval.Universe())
	thread.SetLocal(model_starlark.ValueDecodingOptionsKey, decodingOptions)

	// Construct values that are large enough to be split across
	// multiple objects.
	var listElements []eval.Value
	dict := eval.NewDict()
	set := eval.NewSet()
	for i := 0; i < 1000; i++ {
		listElements = append(listElements, eval.String(fmt.Sprintf("element %d", i)))
		// Insert entries in an order that differs from the
		// sorted order.
		j := (i * 7) % 1000
		require.NoError(t, dict.SetKey(thread, eval.String(fmt.Sprintf("key %d", j)), eval.MakeInt(j)))
		require.NoError(t, set.Add(thread, eval.MakeInt(j)))
	}
	list := eval.NewList(listElements)
	nested := eval.NewList([]eval.Value{
		eval.None,
		eval.True,
		eval.NewTuple([]eval.Value{eval.MakeInt(1), eval.String("two")}),
		list,
		dict,
		set,
		eval.NewDict(),
		eval.NewSet(),
	})

	roundTrip := func(t *testing.T, value eval.Value) (eval.Value, model_core.Message[*model_starlark_pb.Value, object.LocalReference]) {
		encoded, err := model_starlark.EncodeValue(thread, value, nil, nil, encodingOptions)
		require.NoError(t, err)
		topLevel, _ := encoded.SortAndSetReferences()
		message := topLevel.Decay()
		decoded, err := model_starlark.DecodeValue[object.LocalReference, model_core.NoopReferenceMetadata](thread, message, nil)
		require.NoError(t, err)
		return decoded, message
	}

	t.Run("Repr", func(t *testing.T) {
		decoded, _ := roundTrip(t, nested)
		want, err := eval.Repr(thread, nested)
		require.NoError(t, err)
		got, err := eval.Repr(thread, decoded)
		require.NoError(t, err)
		require.Equal(t, want, got)
	})

	t.Run("Equal", func(t *testing.T) {
		decoded, _ := roundTrip(t, nested)
		for _, pair := range [][2]eval.Value{{nested, decoded}, {decoded, nested}, {decoded, decoded}} {
			equal, err := eval.Equal(thread, pair[0], pair[1])
			require.NoError(t, err)
			require.True(t, equal)
		}

		// Values of different types should not be equal.
		decodedSet, _ := roundTrip(t, set)
		equal, err := eval.Equal(thread, decodedSet, list)
		require.NoError(t, err)
		require.False(t, equal)
	})

	t.Run("ListIndex", func(t *testing.T) {
		decoded, _ := roundTrip(t, list)
		decodedList := decoded.(eval.List)
		n, err := decodedList.Len(thread)
		require.NoError(t, err)
		require.Equal(t, 1000, n)
		for i, want := range listElements {
			got, err := decodedList.Index(thread, i)
			require.NoError(t, err)
			require.Equal(t, want, got)
		}
	})

	t.Run("Reencode", func(t *testing.T) {
		// Encoding a value that is backed by storage should
		// yield the original message, without creating any
		// new objects.
		decoded, message := roundTrip(t, nested)
		objectsBefore := len(store.objects)
		reencoded, err := model_starlark.EncodeValue(thread, decoded, nil, nil, encodingOptions)
		require.NoError(t, err)
		require.Equal(t, objectsBefore, len(store.objects))
		topLevel, _ := reencoded.SortAndSetReferences()
		testutil.RequireEqualProto(t, message.Message, topLevel.Decay().Message)

		// Elements of the list are themselves backed by
		// storage, so wrapping them in a new list should only
		// require the outer list to be encoded.
		var elements []eval.Value
		var errIter error
		for element := range decoded.(eval.Iterable).Iterate(thread, &errIter) {
			elements = append(elements, element)
		}
		require.NoError(t, errIter)
		reencoded, err = model_starlark.EncodeValue(thread, eval.NewList(elements), nil, nil, encodingOptions)
		require.NoError(t, err)
		topLevel, _ = reencoded.SortAndSetReferences()
		testutil.RequireEqualProto(t, message.Message, topLevel.Decay().Message)
	})

	t.Run("Starlark", func(t *testing.T) {
		// Run Starlark code against decoded values, to check
		// that they can be used interchangeably with in-memory
		// values.
		decodedList, _ := roundTrip(t, list)
		decodedDict, _ := roundTrip(t, dict)
		decodedSet, _ := roundTrip(t, set)
		decodedTuple, _ := roundTrip(t, eval.NewTuple([]eval.Value{eval.MakeInt(1), eval.String("two")}))
		predeclared := map[string]eval.Value{
			"l": decodedList,
			"d": decodedDict,
			"s": decodedSet,
			"t": decodedTuple,
		}
		source := `
def check(condition, message):
    if not condition:
        fail(message)

def test():
    check(len(l) == 1000, "len(l)")
    check(l, "bool(l)")
    check(l[0] == "element 0", "l[0]")
    check(l[-1] == "element 999", "l[-1]")
    check(l[10:13] == ["element 10", "element 11", "element 12"], "l[10:13]")
    check(l[::-500] == ["element 999", "element 499"], "l[::-500]")
    check("element 500" in l, "in l")
    check(l.index("element 600") == 600, "l.index()")
    check(len([x for x in l]) == 1000, "iterating l")
    check(l + ["extra"] == list(l) + ["extra"], "l + list")
    check(type(l) == "list", "type(l)")
    check(l == list(l), "l == list(l)")
    check(l < list(l) + ["extra"], "l < list")

    check(len(d) == 1000, "len(d)")
    check(d["key 7"] == 7, "d[k]")
    check(d.get("key 1000") == None, "d.get()")
    check("key 999" in d, "in d")
    check(list(d)[:3] == ["key 0", "key 7", "key 14"], "insertion order of d")
    check(d.items()[1] == ("key 7", 7), "d.items()")
    check({"key 0": 0, "new": 1} | d != d, "d | dict")
    check(dict(d) == d, "dict(d) == d")
    check(type(d) == "dict", "type(d)")

    check(len(s) == 1000, "len(s)")
    check(7 in s, "in s")
    check(1000 not in s, "not in s")
    check(list(s)[:3] == [0, 7, 14], "insertion order of s")
    check(s == set(list(s)), "s == set")
    check(s >= set([1, 2]), "s >= set")
    check(s.union([1000]) != s, "s.union()")
    check(type(s) == "set", "type(s)")

    check(t == (1, "two"), "t")
    check({t: 1}[(1, "two")] == 1, "hash(t)")

test()
`
		f, err := syntax.Parse("test.bzl", []byte(source), syntax.Options{})
		require.NoError(t, err)
		universe := eval.Universe()
		m, err := resolve.File(
			f,
			&resolve.BzlDialect,
			func(name string) bool { _, ok := predeclared[name]; return ok },
			func(name string) bool { _, ok := universe[name]; return ok },
		)
		require.NoError(t, err)
		require.NoError(t, eval.ExecFile(thread, compile.File(f, m), eval.NewInMemoryModule("test.bzl", predeclared, nil)))
	})

	t.Run("Mutation", func(t *testing.T) {
		decodedList, _ := roundTrip(t, list)
		require.EqualError(t, decodedList.(eval.List).Append(thread, eval.None), "trying to mutate a frozen list value")
		decodedDict, _ := roundTrip(t, dict)
		require.EqualError(t, decodedDict.(eval.Dict).SetKey(thread, eval.None, eval.None), "trying to mutate a frozen dict value")
		decodedSet, _ := roundTrip(t, set)
		require.EqualError(t, decodedSet.(eval.Set).Add(thread, eval.None), "trying to mutate a frozen set value")
	})

	t.Run("Hash", func(t *testing.T) {
		// Lists, dicts and sets are not hashable, regardless
		// of whether they are backed by storage.
		for _, value := range []eval.Value{list, dict, set} {
			decoded, _ := roundTrip(t, value)
			_, _, err := eval.NewDict().Get(thread, decoded)
			require.ErrorContains(t, err, "unhashable type: "+value.Type())
		}
	})
}
