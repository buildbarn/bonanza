package starlark_test

import (
	"strings"
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
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

	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/proto"
)

func TestStringStorage(t *testing.T) {
	ctx := t.Context()
	referenceFormat := util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1))
	encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
	store := &inMemoryObjectStore{objects: map[object.LocalReference]*object.Contents{}}
	fileCreationParameters, err := model_filesystem.NewFileCreationParametersFromProto(
		&model_filesystem_pb.FileCreationParameters{
			Access:                           &model_filesystem_pb.FileAccessParameters{},
			ChunkMinimumSizeBytes:            1 << 12,
			ChunkHorizonSizeBytes:            1 << 14,
			FileContentsListMinimumSizeBytes: 1 << 10,
			FileContentsListMaximumSizeBytes: 1 << 12,
		},
		referenceFormat,
		model_encoding.NewBinaryEncoderFactory(true),
	)
	require.NoError(t, err)

	newEncodingOptions := func(fileCreationParameters *model_filesystem.FileCreationParameters) *model_starlark.ValueEncodingOptions[object.LocalReference, model_core.NoopReferenceMetadata] {
		options := &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.NoopReferenceMetadata]{
			Context:                ctx,
			ObjectEncoder:          encoder,
			ObjectReferenceFormat:  referenceFormat,
			ObjectCapturer:         store,
			ObjectMinimumSizeBytes: 1024,
			ObjectMaximumSizeBytes: 4096,
			FileCreationParameters: fileCreationParameters,
		}
		return options
	}
	encodingOptions := newEncodingOptions(fileCreationParameters)

	fileReader := model_filesystem.NewFileReader(
		model_parser.NewParsedObjectReader(
			store,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](fileCreationParameters.GetFileContentsListEncoder()),
				model_filesystem.NewFileContentsListObjectParser[object.LocalReference](),
			),
		),
		model_parser.NewParsedObjectReader(
			store,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](fileCreationParameters.GetChunkEncoder()),
				model_parser.NewRawObjectParser[object.LocalReference](),
			),
		),
		semaphore.NewWeighted(1),
	)
	newThread := func(t *testing.T) *eval.Thread {
		thread := newTestThread(t)
		thread.MaximumLargeStringConversionBytes = 1 << 30
		thread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{
			Readers: &model_starlark.ValueReaders[object.LocalReference]{
				Dict: newProtoListReader[model_starlark_pb.Dict_Entry](store, encoder),
				List: newProtoListReader[model_starlark_pb.List_Element](store, encoder),

				FileReader: fileReader,
			},
		})
		return thread
	}

	encode := func(t *testing.T, thread *eval.Thread, value eval.Value) model_core.Message[*model_starlark_pb.Value, object.LocalReference] {
		encoded, err := model_starlark.EncodeValue(thread, value, nil, nil, encodingOptions)
		require.NoError(t, err)
		topLevel, _ := encoded.SortAndSetReferences()

		// Strings may contain arbitrary bytes, so the resulting
		// message must be marshalable regardless of whether the
		// string is valid UTF-8.
		_, err = proto.Marshal(topLevel.Message)
		require.NoError(t, err)
		return topLevel.Decay()
	}
	roundTrip := func(t *testing.T, thread *eval.Thread, value eval.Value) (eval.Value, model_core.Message[*model_starlark_pb.Value, object.LocalReference]) {
		message := encode(t, thread, value)
		decoded, err := model_starlark.DecodeValue[object.LocalReference, model_core.NoopReferenceMetadata](thread, message, nil)
		require.NoError(t, err)
		return decoded, message
	}

	t.Run("SmallInvalidUTF8", func(t *testing.T) {
		// Octal escapes yield single bytes, meaning strings like
		// "\200" are not valid UTF-8. These are used by
		// @bazel_lib//lib/private:strings.bzl.
		thread := newThread(t)
		decoded, message := roundTrip(t, thread, eval.String("\200"))
		require.Equal(t, eval.String("\200"), decoded)
		testutil.RequireEqualProto(t, newStringValue("\200"), message.Message)

		// Such strings can only be obtained as bytes, as Go
		// strings are expected to contain UTF-8.
		_, _, err := eval.AsString(thread, decoded)
		require.ErrorContains(t, err, "not valid UTF-8")
		b, ok, err := eval.AsBytes(thread, decoded)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, []byte("\200"), b)
		_, _, err = model_starlark.GetString(ctx, fileReader, message, 100)
		require.ErrorContains(t, err, "not valid UTF-8")
	})

	t.Run("Empty", func(t *testing.T) {
		thread := newThread(t)
		decoded, message := roundTrip(t, thread, eval.String(""))
		require.Equal(t, eval.String(""), decoded)
		testutil.RequireEqualProto(t, newStringValue(""), message.Message)
	})

	t.Run("InlineLimit", func(t *testing.T) {
		// Strings up to the limit should be stored inline.
		thread := newThread(t)
		decoded, message := roundTrip(t, thread, eval.String(strings.Repeat("x", 1024)))
		require.Equal(t, eval.String(strings.Repeat("x", 1024)), decoded)
		testutil.RequireEqualProto(t, newStringValue(strings.Repeat("x", 1024)), message.Message)

		decoded, message = roundTrip(t, thread, eval.String(strings.Repeat("x", 1025)))
		require.IsType(t, &eval.LargeString{}, decoded)

		// Reading strings that are not stored inline requires a
		// reader.
		_, _, err := model_starlark.GetString[object.LocalReference](ctx, nil, message, 2000)
		require.ErrorContains(t, err, "cannot be read from within this context")
		s, ok, err := model_starlark.GetString(ctx, fileReader, message, 2000)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, strings.Repeat("x", 1025), s)
		_, _, err = model_starlark.GetString(ctx, fileReader, message, 1000)
		require.ErrorContains(t, err, "exceeds the permitted maximum")
	})

	// A string that is large enough to be split into multiple
	// chunks, containing bytes that are not valid UTF-8.
	var sb strings.Builder
	for i := 0; sb.Len() < 100000; i++ {
		sb.WriteString(strings.Repeat(string(rune('a'+i%26)), i%100))
		sb.WriteString("\200é")
	}
	data := sb.String()

	t.Run("Large", func(t *testing.T) {
		thread := newThread(t)
		decoded, message := roundTrip(t, thread, eval.String(data))
		largeString, ok := decoded.(*eval.LargeString)
		require.True(t, ok)
		require.IsType(t, &model_filesystem_pb.FileContents_List_{}, message.Message.GetStr().GetContentsExternal().GetLevel())

		// The large string should behave identically to the
		// original.
		n, err := largeString.Len(thread)
		require.NoError(t, err)
		require.Equal(t, len(data), n)
		equal, err := eval.Equal(thread, decoded, eval.String(data))
		require.NoError(t, err)
		require.True(t, equal)
		got, err := eval.Repr(thread, decoded)
		require.NoError(t, err)
		require.Equal(t, syntax.Quote(data), got)
		wantHash, err := eval.Hash(thread, eval.String(data))
		require.NoError(t, err)
		gotHash, err := eval.Hash(thread, decoded)
		require.NoError(t, err)
		require.Equal(t, wantHash, gotHash)
		slice, err := largeString.Slice(thread, 50000, 50100, 1)
		require.NoError(t, err)
		require.Equal(t, eval.String(data[50000:50100]), slice)
		converted, err := largeString.ToString(thread)
		require.NoError(t, err)
		require.Equal(t, eval.String(data), converted)

		// Strings that are decoded from the same message should
		// be equal, even if their contents can't be read.
		otherDecoded, err := model_starlark.DecodeValue[object.LocalReference, model_core.NoopReferenceMetadata](thread, message, nil)
		require.NoError(t, err)
		blindThread := newTestThread(t)
		blindThread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{})
		equal, err = eval.Equal(blindThread, decoded, otherDecoded)
		require.NoError(t, err)
		require.True(t, equal)

		// Encoding a large string that is backed by storage
		// should yield the original message, without creating
		// any new objects.
		objectsBefore := len(store.objects)
		reencoded := encode(t, thread, decoded)
		require.Equal(t, objectsBefore, len(store.objects))
		testutil.RequireEqualProto(t, message.Message, reencoded.Message)

		// Encoding is canonical: encoding the same string again
		// yields the same message.
		testutil.RequireEqualProto(t, message.Message, encode(t, thread, eval.String(data)).Message)
	})

	t.Run("LargeWithoutFileCreationParameters", func(t *testing.T) {
		thread := newThread(t)
		_, err := model_starlark.EncodeValue(thread, eval.String(data), nil, nil, newEncodingOptions(nil))
		require.ErrorContains(t, err, "cannot be encoded from within this context")
	})

	t.Run("LargeWithoutFileReader", func(t *testing.T) {
		thread := newThread(t)
		message := encode(t, thread, eval.String(data))
		blindThread := newTestThread(t)
		blindThread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{
			Readers: &model_starlark.ValueReaders[object.LocalReference]{},
		})
		decoded, err := model_starlark.DecodeValue[object.LocalReference, model_core.NoopReferenceMetadata](blindThread, message, nil)
		require.NoError(t, err)
		_, err = eval.Repr(blindThread, decoded)
		require.ErrorContains(t, err, "cannot be read from within this context")
	})

	t.Run("CompiledProgram", func(t *testing.T) {
		// Both globals and constants in function code may
		// contain strings that are not valid UTF-8.
		filename := util.Must(label.NewCanonicalLabel("@@rules+//:strings.bzl"))
		f, err := syntax.Parse(filename.String(), []byte(`
CHAR_TO_INT = {"\200": 128, "\377": 255}

def char_to_int(c):
    return {"\200": 128}[c]
`), syntax.Options{})
		require.NoError(t, err)
		universe := eval.Universe()
		m, err := resolve.File(
			f,
			&resolve.BzlDialect,
			func(name string) bool { return false },
			func(name string) bool { _, ok := universe[name]; return ok },
		)
		require.NoError(t, err)
		program := compile.File(f, m)
		thread := newThread(t)
		module := model_starlark.NewBzlModule(filename, nil, nil)
		require.NoError(t, eval.ExecFile(thread, program, module))

		encodedProgram, err := model_starlark.EncodeCompiledProgram(thread, program, module.Globals(), encodingOptions)
		require.NoError(t, err)
		topLevel, _ := encodedProgram.SortAndSetReferences()
		_, err = proto.Marshal(topLevel.Message)
		require.NoError(t, err)
		require.Equal(t, []string{"CHAR_TO_INT", "char_to_int"}, topLevel.Message.Globals.Keys)
	})
}
