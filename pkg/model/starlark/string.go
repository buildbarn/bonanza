package starlark

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"
)

// MaybeInlineFileContentsReader is used to read the contents of large
// strings that are stored in the same way as file contents. It is
// implemented by model_filesystem.FileReader.
type MaybeInlineFileContentsReader[TReference any] interface {
	MaybeInlineFileContentsReadAll(ctx context.Context, m model_core.Message[*model_filesystem_pb.MaybeInlineFileContents, TReference], maximumSizeBytes uint64) ([]byte, error)
	MaybeInlineFileContentsReadAt(ctx context.Context, m model_core.Message[*model_filesystem_pb.MaybeInlineFileContents, TReference], p []byte, offsetBytes uint64) (int, error)
}

// encodeString converts a string to a Value message. Strings that are
// small enough are stored inline. Larger strings are stored in the same
// way as file contents.
func encodeString[TReference any, TMetadata model_core.ReferenceMetadata](s string, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	if len(s) > options.ObjectMinimumSizeBytes && options.FileCreationParameters == nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, errors.New("strings that are too large to be stored inline cannot be encoded from within this context")
	}
	contents, err := model_filesystem.CreateMaybeInlineFileContents(
		options.Context,
		options.FileCreationParameters,
		[]byte(s),
		options.ObjectMinimumSizeBytes,
		model_filesystem.NewSimpleFileMerkleTreeCapturer[TMetadata](options.ObjectCapturer),
	)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
	}
	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_starlark_pb.Value, error) {
		return &model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Str{
				Str: contents.Merge(patcher),
			},
		}, nil
	})
}

// encodeLargeString converts a large string to a Value message. If the
// string is backed by storage, the original message is returned.
func encodeLargeString[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, s *eval.LargeString, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	if source, ok := s.Source().(EncodableValue[TReference, TMetadata]); ok {
		return source.EncodeValue(thread, nil, nil, options)
	}
	contents := make([]byte, s.Source().SizeBytes())
	if err := s.Source().ReadAt(thread, contents, 0); err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
	}
	return encodeString(string(contents), options)
}

// GetString returns the contents of a Value message containing a
// string. Strings that are too large to be stored inline are read using
// the provided reader, which may be nil if the caller knows that no
// such strings exist. As the contents are returned as a Go string, an
// error is returned if they are not valid UTF-8.
//
// This function can be used by code that inspects encoded values
// directly. If the message does not contain a string, ok is false.
func GetString[TReference any](ctx context.Context, reader MaybeInlineFileContentsReader[TReference], value model_core.Message[*model_starlark_pb.Value, TReference], maximumSizeBytes uint64) (s string, ok bool, err error) {
	str, ok := value.Message.GetKind().(*model_starlark_pb.Value_Str)
	if !ok {
		return "", false, nil
	}
	var contents []byte
	switch level := str.Str.GetLevel().(type) {
	case nil:
	case *model_filesystem_pb.MaybeInlineFileContents_ContentsInline:
		if uint64(len(level.ContentsInline)) > maximumSizeBytes {
			return "", true, fmt.Errorf("string is %d bytes in size, which exceeds the permitted maximum of %d bytes", len(level.ContentsInline), maximumSizeBytes)
		}
		contents = level.ContentsInline
	case *model_filesystem_pb.MaybeInlineFileContents_ContentsExternal:
		if reader == nil {
			return "", true, errors.New("strings that are too large to be stored inline cannot be read from within this context")
		}
		contents, err = reader.MaybeInlineFileContentsReadAll(ctx, model_core.Nested(value, str.Str), maximumSizeBytes)
		if err != nil {
			return "", true, err
		}
	default:
		return "", true, errors.New("invalid string contents")
	}
	if !utf8.Valid(contents) {
		return "", true, errors.New("string contains bytes that are not valid UTF-8")
	}
	return string(contents), true, nil
}

// decodeString converts a Value message containing a string to a
// Starlark value. Strings that are stored inline are returned as
// eval.String. Larger strings are returned as eval.LargeString, whose
// contents are only read when needed.
func decodeString[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](value model_core.Message[*model_starlark_pb.Value, TReference], contents *model_filesystem_pb.MaybeInlineFileContents) (eval.Value, error) {
	switch level := contents.GetLevel().(type) {
	case nil:
		return eval.String(""), nil
	case *model_filesystem_pb.MaybeInlineFileContents_ContentsInline:
		return eval.String(level.ContentsInline), nil
	case *model_filesystem_pb.MaybeInlineFileContents_ContentsExternal:
		sizeBytes := level.ContentsExternal.GetTotalSizeBytes()
		if sizeBytes == 0 || sizeBytes > uint64(int(^uint(0)>>1)) {
			return nil, errors.New("invalid string size")
		}
		return eval.NewLargeString(&storedStringSource[TReference, TMetadata]{
			value:     value,
			contents:  model_core.Nested(value, contents),
			sizeBytes: int64(sizeBytes),
		}), nil
	default:
		return nil, errors.New("invalid string contents")
	}
}

// storedStringSource provides the contents of a large string that is
// stored in the same way as file contents.
type storedStringSource[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	value     model_core.Message[*model_starlark_pb.Value, TReference]
	contents  model_core.Message[*model_filesystem_pb.MaybeInlineFileContents, TReference]
	sizeBytes int64
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*storedStringSource[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ eval.LargeStringSourceComparer                                      = (*storedStringSource[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

func (s *storedStringSource[TReference, TMetadata]) SizeBytes() int64 {
	return s.sizeBytes
}

func (s *storedStringSource[TReference, TMetadata]) ReadAt(t *eval.Thread, p []byte, off int64) error {
	options, err := getValueDecodingOptions[TReference](t)
	if err != nil {
		return err
	}
	if options.Readers.FileReader == nil {
		return errors.New("strings that are too large to be stored inline cannot be read from within this context")
	}
	_, err = options.Readers.FileReader.MaybeInlineFileContentsReadAt(t.Context(), s.contents, p, uint64(off))
	return err
}

// EncodeValue returns the original message of the string, so that it
// doesn't need to be read and stored once more.
func (s *storedStringSource[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	return model_core.Patch(options.ObjectCapturer, s.value), nil
}

type storedStringFileContentsUnpackerInto[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct{}

// NewStoredStringFileContentsUnpackerInto creates an unpacker that
// accepts strings that are stored in the same way as file contents,
// and yields the FileContents message of the string. All other values,
// including strings that are held in memory, are rejected.
//
// Large strings are stored using the same algorithm and parameters as
// files. Builtins that write strings to files can therefore use this
// unpacker in combination with unpack.Or() and unpack.Bytes to reuse
// the existing Merkle tree, without reading the string. The resulting
// message is identical to the one that would be obtained by creating a
// new Merkle tree for the contents of the string.
func NewStoredStringFileContentsUnpackerInto[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata]() unpack.UnpackerInto[model_core.Message[*model_filesystem_pb.FileContents, TReference]] {
	return storedStringFileContentsUnpackerInto[TReference, TMetadata]{}
}

func (storedStringFileContentsUnpackerInto[TReference, TMetadata]) getSource(v eval.Value) (*storedStringSource[TReference, TMetadata], *model_filesystem_pb.MaybeInlineFileContents_ContentsExternal, error) {
	if s, ok := v.(*eval.LargeString); ok {
		if source, ok := s.Source().(*storedStringSource[TReference, TMetadata]); ok {
			if external, ok := source.contents.Message.Level.(*model_filesystem_pb.MaybeInlineFileContents_ContentsExternal); ok {
				return source, external, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("got %s, want string that is stored in the same way as file contents", v.Type())
}

func (ui storedStringFileContentsUnpackerInto[TReference, TMetadata]) UnpackInto(thread *eval.Thread, v eval.Value, dst *model_core.Message[*model_filesystem_pb.FileContents, TReference]) error {
	source, external, err := ui.getSource(v)
	if err != nil {
		return err
	}
	*dst = model_core.Nested(source.contents, external.ContentsExternal)
	return nil
}

func (ui storedStringFileContentsUnpackerInto[TReference, TMetadata]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	if _, _, err := ui.getSource(v); err != nil {
		return nil, err
	}
	return v, nil
}

func (storedStringFileContentsUnpackerInto[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return 0
}

// getRootReference returns the reference of the object at the root of
// the Merkle tree containing the contents of the string.
func (s *storedStringSource[TReference, TMetadata]) getRootReference() (model_core.Decodable[object.LocalReference], bool) {
	external, ok := s.contents.Message.Level.(*model_filesystem_pb.MaybeInlineFileContents_ContentsExternal)
	if !ok {
		return model_core.Decodable[object.LocalReference]{}, false
	}
	var root *model_core_pb.DecodableReference
	switch level := external.ContentsExternal.GetLevel().(type) {
	case *model_filesystem_pb.FileContents_ChunkReference:
		root = level.ChunkReference
	case *model_filesystem_pb.FileContents_List_:
		root = level.List.GetReference()
	default:
		return model_core.Decodable[object.LocalReference]{}, false
	}
	reference, err := model_core.FlattenDecodableReference(model_core.Nested(s.contents, root))
	if err != nil {
		return model_core.Decodable[object.LocalReference]{}, false
	}
	localReference, err := model_core.NewDecodable(reference.Value.GetLocalReference(), reference.GetDecodingParameters())
	return localReference, err == nil
}

// EqualSource returns true if two large strings that are backed by
// storage refer to the same object. In that case they can be
// considered equal without reading them.
func (s *storedStringSource[TReference, TMetadata]) EqualSource(other eval.LargeStringSource) bool {
	otherSource, ok := other.(*storedStringSource[TReference, TMetadata])
	if !ok || s.sizeBytes != otherSource.sizeBytes {
		return false
	}
	reference, ok := s.getRootReference()
	if !ok {
		return false
	}
	otherReference, ok := otherSource.getRootReference()
	return ok && reference == otherReference
}
