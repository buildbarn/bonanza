package starlark_test

import (
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/types/known/emptypb"
)

func TestAttrValueBuilder(t *testing.T) {
	str := func(s string) *model_starlark_pb.Value {
		return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Str{Str: s}}
	}
	labelValue := func(s string) *model_starlark_pb.Value {
		return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Label{Label: s}}
	}
	list := func(values ...*model_starlark_pb.Value) *model_starlark_pb.Value {
		elements := make([]*model_starlark_pb.List_Element, 0, len(values))
		for _, value := range values {
			elements = append(elements, &model_starlark_pb.List_Element{Level: &model_starlark_pb.List_Element_Leaf{Leaf: value}})
		}
		return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_List{List: &model_starlark_pb.List{Elements: elements}}}
	}
	dict := func(keysAndValues ...*model_starlark_pb.Value) *model_starlark_pb.Value {
		var entries []*model_starlark_pb.Dict_Entry
		for i := 0; i < len(keysAndValues); i += 2 {
			entries = append(entries, &model_starlark_pb.Dict_Entry{Level: &model_starlark_pb.Dict_Entry_Leaf_{
				Leaf: &model_starlark_pb.Dict_Entry_Leaf{Key: keysAndValues[i], Value: keysAndValues[i+1]},
			}})
		}
		return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Dict{Dict: &model_starlark_pb.Dict{Entries: entries}}}
	}

	threadOptions := &model_starlark.ValueDecodingOptions[object.LocalReference]{
		Readers: &model_starlark.ValueReaders[object.LocalReference]{},
		LabelCreator: func(label.ResolvedLabel) (eval.Value, error) {
			return eval.String("from thread options"), nil
		},
	}
	newThread := func() *eval.Thread {
		thread := eval.NewThread(t.Context(), eval.Universe())
		thread.SetLocal(model_starlark.ValueDecodingOptionsKey, threadOptions)
		return thread
	}
	partOptions := &model_starlark.ValueDecodingOptions[object.LocalReference]{
		Readers: &model_starlark.ValueReaders[object.LocalReference]{},
		LabelCreator: func(resolvedLabel label.ResolvedLabel) (eval.Value, error) {
			return eval.String("target " + resolvedLabel.String()), nil
		},
	}
	decode := func(t *testing.T, thread *eval.Thread, value *model_starlark_pb.Value) model_starlark.AttrValuePart {
		part, err := model_starlark.DecodeAttrValuePart[object.LocalReference, model_core.NoopReferenceMetadata](
			thread,
			model_core.NewSimpleMessage[object.LocalReference](value),
			partOptions,
		)
		require.NoError(t, err)
		return part
	}
	build := func(t *testing.T, thread *eval.Thread, values ...*model_starlark_pb.Value) (eval.Value, error) {
		var b model_starlark.AttrValueBuilder
		for _, value := range values {
			if err := b.Add(thread, decode(t, thread, value)); err != nil {
				return nil, err
			}
		}
		return b.Build(thread)
	}
	repr := func(t *testing.T, thread *eval.Thread, value eval.Value) string {
		s, err := eval.Repr(thread, value)
		require.NoError(t, err)
		return s
	}

	t.Run("Empty", func(t *testing.T) {
		thread := newThread()
		value, err := build(t, thread)
		require.NoError(t, err)
		require.Nil(t, value)
	})

	t.Run("ConcatenateLists", func(t *testing.T) {
		thread := newThread()
		value, err := build(t, thread, list(str("a"), str("b")), list(), list(str("c")))
		require.NoError(t, err)
		require.Equal(t, `["a", "b", "c"]`, repr(t, thread, value))

		// The resulting value should be immutable.
		require.ErrorContains(t, value.(eval.List).Append(thread, eval.None), "frozen")
	})

	t.Run("ConcatenateDicts", func(t *testing.T) {
		// Similar to the | operator, later entries should
		// overwrite earlier ones, while preserving their
		// position.
		thread := newThread()
		value, err := build(t, thread, dict(str("a"), str("1"), str("b"), str("2")), dict(str("c"), str("3"), str("a"), str("4")))
		require.NoError(t, err)
		require.Equal(t, `{"a": "4", "b": "2", "c": "3"}`, repr(t, thread, value))
		require.ErrorContains(t, value.(eval.Dict).SetKey(thread, eval.None, eval.None), "frozen")
	})

	t.Run("ConcatenateStrings", func(t *testing.T) {
		thread := newThread()
		value, err := build(t, thread, str("a"), str("b"), str("c"))
		require.NoError(t, err)
		require.Equal(t, eval.String("abc"), value)
	})

	t.Run("IncompatibleTypes", func(t *testing.T) {
		thread := newThread()
		_, err := build(t, thread, list(str("a")), str("b"))
		require.Error(t, err)
	})

	t.Run("Labels", func(t *testing.T) {
		// Labels should be converted using the provided
		// options, both when they are the value itself and when
		// they are contained in a list or dict. The options of
		// the thread should be left untouched.
		thread := newThread()
		for value, expected := range map[*model_starlark_pb.Value]string{
			labelValue("@@a+//:b"):                 `"target @@a+//:b"`,
			list(labelValue("@@a+//:b")):           `["target @@a+//:b"]`,
			dict(labelValue("@@a+//:b"), str("c")): `{"target @@a+//:b": "c"}`,
			dict(str("c"), labelValue("@@a+//:b")): `{"c": "target @@a+//:b"}`,
		} {
			built, err := build(t, thread, value)
			require.NoError(t, err)
			require.Equal(t, expected, repr(t, thread, built))
		}
		require.Same(t, threadOptions, thread.Local(model_starlark.ValueDecodingOptionsKey))
	})

	t.Run("NestedLists", func(t *testing.T) {
		// Values of attributes of type string_list_dict contain
		// nested lists, which should remain readable.
		thread := newThread()
		value, err := build(t, thread, dict(str("a"), list(str("b"), str("c"))))
		require.NoError(t, err)
		require.Equal(t, `{"a": ["b", "c"]}`, repr(t, thread, value))
	})

	t.Run("AsList", func(t *testing.T) {
		thread := newThread()
		none := &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_None{None: &emptypb.Empty{}}}

		for _, tc := range []struct {
			value    *model_starlark_pb.Value
			omitNone bool
			expected string
		}{
			{str("a"), false, `["a"]`},
			{str("a"), true, `["a"]`},
			{none, false, `[None]`},
			{none, true, `[]`},
		} {
			part, err := decode(t, thread, tc.value).AsList(thread, tc.omitNone)
			require.NoError(t, err)
			value, err := part.Value(thread)
			require.NoError(t, err)
			require.Equal(t, tc.expected, repr(t, thread, value))
		}
	})

	t.Run("SamePartInMultipleBuilders", func(t *testing.T) {
		// Extending one builder should not affect the other,
		// even though both started out using the same part.
		thread := newThread()
		part := decode(t, thread, list(str("a")))
		var b1, b2 model_starlark.AttrValueBuilder
		require.NoError(t, b1.Add(thread, part))
		require.NoError(t, b2.Add(thread, part))
		require.NoError(t, b1.Add(thread, decode(t, thread, list(str("b")))))
		require.NoError(t, b2.Add(thread, decode(t, thread, list(str("c")))))

		v1, err := b1.Build(thread)
		require.NoError(t, err)
		v2, err := b2.Build(thread)
		require.NoError(t, err)
		require.Equal(t, `["a", "b"]`, repr(t, thread, v1))
		require.Equal(t, `["a", "c"]`, repr(t, thread, v2))
	})
}
