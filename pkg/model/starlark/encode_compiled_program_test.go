package starlark_test

import (
	"context"
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark "bonanza.build/pkg/model/starlark"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

// discardingObjectManager is an ObjectManager that does not store any
// objects. It is used in combination with LeakCheckingObjectManager to
// test that encoding values does not leak any metadata.
type discardingObjectManager struct{}

func (discardingObjectManager) CaptureCreatedObject(ctx context.Context, createdObject model_core.CreatedObject[model_core.NoopReferenceMetadata]) (model_core.NoopReferenceMetadata, error) {
	return model_core.NoopReferenceMetadata{}, nil
}

func (discardingObjectManager) CaptureExistingObject(object.LocalReference) model_core.NoopReferenceMetadata {
	return model_core.NoopReferenceMetadata{}
}

func (discardingObjectManager) ReferenceObject(entry model_core.MetadataEntry[model_core.NoopReferenceMetadata]) object.LocalReference {
	return entry.LocalReference
}

func TestEncodeCompiledProgramNoLeaks(t *testing.T) {
	// Values that refer to functions, such as rules and
	// providers, store the code of these functions in separate
	// objects. Encoding them must not leak the metadata of these
	// objects.
	type metadata = *model_core.LeakCheckingReferenceMetadata[model_core.NoopReferenceMetadata]
	filename := util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl"))
	source := `
def _impl(ctx):
    pass

def _init(**kwargs):
    return kwargs

def _computed(self):
    return 42

def _transition_impl(settings, attr):
    return {}

MyInfo, _new_my_info = provider(init = _init, computed_fields = {"answer": _computed})

my_aspect = aspect(implementation = _impl)

my_macro = macro(implementation = lambda name, visibility: None)

my_module_extension = module_extension(
    implementation = _impl,
    tag_classes = {"tag": tag_class(attrs = {"value": attr.string()})},
)

my_repository_rule = repository_rule(
    implementation = _impl,
    attrs = {"value": attr.string(default = "x")},
)

my_rule = rule(implementation = _impl, attrs = {"value": attr.string()})

my_subrule = subrule(implementation = _impl)

my_transition = transition(implementation = _transition_impl, inputs = [], outputs = [])
`
	bzlFileBuiltins, _ := model_starlark.GetBuiltins[object.LocalReference, metadata]()
	f, err := syntax.Parse(filename.String(), []byte(source), syntax.Options{})
	require.NoError(t, err)
	universe := eval.Universe()
	m, err := resolve.File(
		f,
		&resolve.BzlDialect,
		func(name string) bool { _, ok := bzlFileBuiltins[name]; return ok },
		func(name string) bool { _, ok := universe[name]; return ok },
	)
	require.NoError(t, err)
	program := compile.File(f, m)
	thread := newTestThread(t)
	module := model_starlark.NewBzlModule(filename, bzlFileBuiltins, nil)
	require.NoError(t, eval.ExecFile(thread, program, module))
	globals := module.Globals()

	objectManager := model_core.NewLeakCheckingObjectManager[object.LocalReference, model_core.NoopReferenceMetadata](discardingObjectManager{})
	encodedProgram, err := model_starlark.EncodeCompiledProgram(thread, program, globals, &model_starlark.ValueEncodingOptions[object.LocalReference, metadata]{
		CurrentFilename:        &filename,
		Context:                t.Context(),
		ObjectEncoder:          model_encoding.NewChainedDeterministicBinaryEncoder(nil),
		ObjectReferenceFormat:  object.SHA256V1ReferenceFormat,
		ObjectCapturer:         objectManager,
		ObjectMinimumSizeBytes: 1024,
		ObjectMaximumSizeBytes: 4096,
	})
	require.NoError(t, err)
	require.Len(t, encodedProgram.Message.Globals.Keys, 9)

	// All metadata should be owned by the resulting message.
	encodedProgram.Discard()
	require.Equal(t, 0, objectManager.GetCurrentValidMetadataCount())
}
