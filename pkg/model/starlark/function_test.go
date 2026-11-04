package starlark_test

import (
	"fmt"
	"strings"
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
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

	"google.golang.org/protobuf/proto"
)

func TestFunctionStorage(t *testing.T) {
	filename := util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl"))
	otherFilename := util.Must(label.NewCanonicalLabel("@@other+//:other.bzl"))
	source := `
load("@other//:other.bzl", "SUFFIX")

_PREFIX = "hello"

def greet(name, punctuation = "!"):
    return _PREFIX + ", " + name + punctuation + SUFFIX

def make_adder(n):
    def add(x):
        return x + n
    return add

def make_multiplier(n):
    return lambda x: x * n

def constants():
    return ["text", 7, 123456789012345678901234567890, 2.5]

adder = make_adder(3)
`

	// Compile and execute the file. Canonicalize the imports, so
	// that the code of functions can be written to storage.
	f, err := syntax.Parse(filename.String(), []byte(source), syntax.Options{})
	require.NoError(t, err)
	bzlFileBuiltins, _ := model_starlark.GetBuiltins[object.LocalReference, model_core.NoopReferenceMetadata]()
	universe := eval.Universe()
	m, err := resolve.File(
		f,
		&resolve.BzlDialect,
		func(name string) bool { _, ok := bzlFileBuiltins[name]; return ok },
		func(name string) bool { _, ok := universe[name]; return ok },
	)
	require.NoError(t, err)
	program := compile.File(f, m)
	require.NoError(t, model_starlark.CanonicalizeImports(program, func(module string) (label.CanonicalLabel, error) {
		require.Equal(t, "@other//:other.bzl", module)
		return otherFilename, nil
	}))
	module := eval.NewInMemoryModule(filename.String(), bzlFileBuiltins, func(t *eval.Thread, imp compile.Import) (eval.Value, error) {
		return eval.String("?"), nil
	})
	require.NoError(t, eval.ExecFile(newTestThread(t), program, module))

	for _, maximumSizeBytes := range []int{1024 * 1024, 64} {
		t.Run(fmt.Sprintf("MaximumSize%d", maximumSizeBytes), func(t *testing.T) {
			// Write the globals of the file to storage. With a
			// small maximum object size, the B-tree containing
			// the code of functions spans multiple objects.
			encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
			store := &inMemoryObjectStore{objects: map[object.LocalReference]*object.Contents{}}
			encodingOptions := &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.NoopReferenceMetadata]{
				CurrentFilename:        &filename,
				Context:                t.Context(),
				ObjectEncoder:          encoder,
				ObjectReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				ObjectCapturer:         store,
				ObjectMinimumSizeBytes: maximumSizeBytes / 4,
				ObjectMaximumSizeBytes: maximumSizeBytes,
			}
			encodingThread := newTestThread(t)
			encodedProgram, err := model_starlark.EncodeCompiledProgram(encodingThread, program, module.Globals(), encodingOptions)
			require.NoError(t, err)
			topLevelProgram, _ := encodedProgram.SortAndSetReferences()
			globals := topLevelProgram.Decay()

			// Only public globals and private globals that are
			// referenced by functions should be encoded.
			require.Equal(t, []string{"_PREFIX", "adder", "constants", "greet", "make_adder", "make_multiplier"}, globals.Message.Globals.Keys)

			// Load the functions on another thread, resolving
			// globals through the GlobalResolver.
			decodingOptions := &model_starlark.ValueDecodingOptions[object.LocalReference]{
				Readers: &model_starlark.ValueReaders[object.LocalReference]{
					CompiledProgramFunctions: newProtoListReader[model_starlark_pb.CompiledProgram_FunctionEntry](store, encoder),
					Dict:                     newProtoListReader[model_starlark_pb.Dict_Entry](store, encoder),
					List:                     newProtoListReader[model_starlark_pb.List_Element](store, encoder),
				},
				BzlFileBuiltins: bzlFileBuiltins,
			}
			thread := newTestThread(t)
			thread.SetLocal(model_starlark.ValueDecodingOptionsKey, decodingOptions)
			decodedGlobals, err := model_starlark.DecodeGlobals[object.LocalReference, model_core.NoopReferenceMetadata](thread, model_core.Nested(globals, globals.Message.Globals), filename)
			require.NoError(t, err)
			thread.SetLocal(model_starlark.CompiledProgramResolverKey, model_starlark.CompiledProgramResolver[object.LocalReference](func(filename label.CanonicalLabel) (model_core.Message[*model_starlark_pb.CompiledProgram, object.LocalReference], error) {
				return globals, nil
			}))
			var resolvedIdentifiers []string
			thread.SetLocal(model_starlark.GlobalResolverKey, model_starlark.GlobalResolver[object.LocalReference](func(identifier label.CanonicalStarlarkIdentifier) (model_core.Message[*model_starlark_pb.Value, object.LocalReference], error) {
				resolvedIdentifiers = append(resolvedIdentifiers, identifier.String())
				if identifier.GetCanonicalLabel() == otherFilename {
					return model_core.NewSimpleMessage[object.LocalReference](newStringValue(".")), nil
				}
				return model_starlark.GetStructFieldValue(t.Context(), decodingOptions.Readers.List, model_core.Nested(globals, globals.Message.Globals), identifier.GetStarlarkIdentifier().String())
			}))

			call := func(fn eval.Value, args ...eval.Value) eval.Value {
				v, err := eval.Call(thread, fn, eval.Args{Positional: args})
				require.NoError(t, err)
				return v
			}

			// Calling a function should resolve the globals and
			// loaded symbols it references. Default values of
			// parameters should be preserved.
			require.Equal(t, eval.String("hello, world!."), call(decodedGlobals["greet"], eval.String("world")))
			require.Equal(t, eval.String("hello, world?."), call(decodedGlobals["greet"], eval.String("world"), eval.String("?")))
			require.Equal(t, []string{"@@rules+//:defs.bzl%_PREFIX", "@@other+//:other.bzl%SUFFIX"}, resolvedIdentifiers)

			// Nested functions should be callable, both if they
			// were created before the program was written to
			// storage, and if they are created from code that was
			// loaded from storage.
			require.Equal(t, eval.MakeInt(7), call(decodedGlobals["adder"], eval.MakeInt(4)))
			require.Equal(t, eval.MakeInt(11), call(call(decodedGlobals["make_adder"], eval.MakeInt(10)), eval.MakeInt(1)))
			require.Equal(t, eval.MakeInt(12), call(call(decodedGlobals["make_multiplier"], eval.MakeInt(3)), eval.MakeInt(4)))

			// Functions should report the lines at which they
			// are declared, both at the top level and when nested.
			funcodeOf := func(fn eval.Value) *compile.Funcode {
				funcode, err := fn.(eval.Function).Funcode(thread)
				require.NoError(t, err)
				return funcode
			}
			require.Equal(t, syntax.Position{Line: 6, Column: 1}, funcodeOf(decodedGlobals["greet"]).Pos)
			require.Equal(t, syntax.Position{Line: 9, Column: 1}, funcodeOf(decodedGlobals["make_adder"]).Pos)
			require.Equal(t, syntax.Position{Line: 10, Column: 5}, funcodeOf(decodedGlobals["adder"]).Pos)
			require.Equal(t, syntax.Position{Line: 15, Column: 12}, funcodeOf(call(decodedGlobals["make_multiplier"], eval.MakeInt(3))).Pos)

			// Constants of all types should be preserved.
			constants, err := eval.Repr(thread, call(decodedGlobals["constants"]))
			require.NoError(t, err)
			require.Equal(t, `["text", 7, 123456789012345678901234567890, 2.5]`, constants)

			// Functions that are backed by storage should be
			// encoded by returning their original message.
			reencoded, err := model_starlark.EncodeValue(thread, decodedGlobals["greet"], nil, nil, encodingOptions)
			require.NoError(t, err)
			reencodedTopLevel, _ := reencoded.SortAndSetReferences()
			originalGreet, err := model_starlark.GetStructFieldValue(t.Context(), decodingOptions.Readers.List, model_core.Nested(globals, globals.Message.Globals), "greet")
			require.NoError(t, err)
			originalTopLevel, _ := model_core.Patch(store, originalGreet).SortAndSetReferences()
			require.True(t, model_core.TopLevelMessagesEqual(originalTopLevel, reencodedTopLevel))
		})
	}
}

func TestFunctionStorageLines(t *testing.T) {
	// Declare enough functions for the B-tree to span multiple
	// levels. Names don't sort in the order of declaration.
	var source strings.Builder
	expectedLines := map[string]int32{}
	for i := range 100 {
		name := fmt.Sprintf("f%d", i)
		expectedLines[name] = int32(3*i + 1)
		fmt.Fprintf(&source, "def %s():\n    return %d\n\n", name, i)
	}

	filename := util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl"))
	encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
	store := &inMemoryObjectStore{objects: map[object.LocalReference]*object.Contents{}}
	encode := func(source string, objectMinimumSizeBytes int) model_core.Message[*model_starlark_pb.CompiledProgram, object.LocalReference] {
		f, err := syntax.Parse(filename.String(), []byte(source), syntax.Options{})
		require.NoError(t, err)
		m, err := resolve.File(
			f,
			&resolve.BzlDialect,
			func(name string) bool { return false },
			func(name string) bool { return false },
		)
		require.NoError(t, err)
		program := compile.File(f, m)
		module := eval.NewInMemoryModule(filename.String(), nil, nil)
		require.NoError(t, eval.ExecFile(newTestThread(t), program, module))
		encodedProgram, err := model_starlark.EncodeCompiledProgram(newTestThread(t), program, module.Globals(), &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.NoopReferenceMetadata]{
			CurrentFilename:        &filename,
			Context:                t.Context(),
			ObjectEncoder:          encoder,
			ObjectReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
			ObjectCapturer:         store,
			ObjectMinimumSizeBytes: objectMinimumSizeBytes,
			ObjectMaximumSizeBytes: objectMinimumSizeBytes * 4,
		})
		require.NoError(t, err)
		topLevel, _ := encodedProgram.SortAndSetReferences()
		return topLevel.Decay()
	}
	decode := func(program model_core.Message[*model_starlark_pb.CompiledProgram, object.LocalReference]) (*eval.Thread, map[string]eval.Value) {
		thread := newTestThread(t)
		thread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{
			Readers: &model_starlark.ValueReaders[object.LocalReference]{
				CompiledProgramFunctions: newProtoListReader[model_starlark_pb.CompiledProgram_FunctionEntry](store, encoder),
				List:                     newProtoListReader[model_starlark_pb.List_Element](store, encoder),
			},
		})
		thread.SetLocal(model_starlark.CompiledProgramResolverKey, model_starlark.CompiledProgramResolver[object.LocalReference](func(label.CanonicalLabel) (model_core.Message[*model_starlark_pb.CompiledProgram, object.LocalReference], error) {
			return program, nil
		}))
		globals, err := model_starlark.DecodeGlobals[object.LocalReference, model_core.NoopReferenceMetadata](thread, model_core.Nested(program, program.Message.Globals), filename)
		require.NoError(t, err)
		return thread, globals
	}

	check := func(program model_core.Message[*model_starlark_pb.CompiledProgram, object.LocalReference], offset int32) {
		thread, globals := decode(program)
		require.Len(t, globals, len(expectedLines))
		for name, line := range expectedLines {
			funcode, err := globals[name].(eval.Function).Funcode(thread)
			require.NoError(t, err, name)
			require.Equal(t, line+offset, funcode.Pos.Line, name)
		}
	}

	original := encode(source.String(), 16)
	require.NotEmpty(t, original.Message.Functions)
	require.NotNil(t, original.Message.Functions[0].GetParent())
	check(original, 0)

	// Adding lines to the start of the file should only affect the
	// list of lines. The objects holding the code of the functions
	// should remain identical, meaning no new objects get created.
	objectsCount := len(store.objects)
	shifted := encode("\n\n\n"+source.String(), 16)
	require.Len(t, store.objects, objectsCount)
	check(shifted, 3)

	t.Run("ExternalGlobalsAndFunctions", func(t *testing.T) {
		// The message is too large, meaning the top-level
		// elements of the B-trees should be moved to separate
		// objects. Keys and line numbers have no external form.
		require.Len(t, original.Message.Functions, 1)
		require.Equal(t, uint32(100), original.Message.Functions[0].GetParent().GetFunctionCount())
		require.Len(t, original.Message.Globals.Values, 1)
		require.NotNil(t, original.Message.Globals.Values[0].GetParent())
		require.Len(t, original.Message.Globals.Keys, 100)
		require.Len(t, original.Message.TopLevelFunctionLines, 100)
	})

	t.Run("InlineGlobalsAndFunctions", func(t *testing.T) {
		// With a sufficiently large object size, everything
		// should fit in a single message.
		inline := encode(source.String(), 64*1024)
		require.Len(t, inline.Message.Functions, 100)
		require.Nil(t, inline.Message.Functions[0].GetParent())
		require.Len(t, inline.Message.Globals.Values, 100)
		require.Nil(t, inline.Message.Globals.Values[0].GetParent())
		check(inline, 0)
	})

	t.Run("MissingLines", func(t *testing.T) {
		thread, globals := decode(model_core.Nested(original, &model_starlark_pb.CompiledProgram{
			Globals:   original.Message.Globals,
			Functions: original.Message.Functions,
		}))
		_, err := globals["f0"].(eval.Function).Funcode(thread)
		require.ErrorContains(t, err, "line of function does not exist")
	})
}

func TestFunctionStorageRecursionCheck(t *testing.T) {
	// Both files contain identical functions at identical positions,
	// meaning that the code of these functions is stored in the same
	// objects. This should not cause them to be considered the same
	// function.
	source := `
def call(f, *args):
    return f(*args)

def factorial(n):
    return n * factorial(n - 1) if n else 1
`
	encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
	store := &inMemoryObjectStore{objects: map[object.LocalReference]*object.Contents{}}
	decodingOptions := &model_starlark.ValueDecodingOptions[object.LocalReference]{
		Readers: &model_starlark.ValueReaders[object.LocalReference]{
			CompiledProgramFunctions: newProtoListReader[model_starlark_pb.CompiledProgram_FunctionEntry](store, encoder),
			List:                     newProtoListReader[model_starlark_pb.List_Element](store, encoder),
		},
	}
	thread := newTestThread(t)
	thread.SetLocal(model_starlark.ValueDecodingOptionsKey, decodingOptions)

	encodedGlobals := map[label.CanonicalLabel]model_core.Message[*model_starlark_pb.CompiledProgram, object.LocalReference]{}
	decodedGlobals := map[label.CanonicalLabel]map[string]eval.Value{}
	filenameA := util.Must(label.NewCanonicalLabel("@@a+//:defs.bzl"))
	filenameB := util.Must(label.NewCanonicalLabel("@@b+//:defs.bzl"))
	for _, filename := range []label.CanonicalLabel{filenameA, filenameB} {
		f, err := syntax.Parse(filename.String(), []byte(source), syntax.Options{})
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
		module := eval.NewInMemoryModule(filename.String(), nil, nil)
		require.NoError(t, eval.ExecFile(newTestThread(t), program, module))

		encodedProgram, err := model_starlark.EncodeCompiledProgram(newTestThread(t), program, module.Globals(), &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.NoopReferenceMetadata]{
			CurrentFilename:        &filename,
			Context:                t.Context(),
			ObjectEncoder:          encoder,
			ObjectReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
			ObjectCapturer:         store,
			ObjectMinimumSizeBytes: 1024,
			ObjectMaximumSizeBytes: 4096,
		})
		require.NoError(t, err)
		topLevel, _ := encodedProgram.SortAndSetReferences()
		encodedGlobals[filename] = topLevel.Decay()
		decodedGlobals[filename], err = model_starlark.DecodeGlobals[object.LocalReference, model_core.NoopReferenceMetadata](thread, model_core.Nested(encodedGlobals[filename], encodedGlobals[filename].Message.Globals), filename)
		require.NoError(t, err)
	}
	thread.SetLocal(model_starlark.GlobalResolverKey, model_starlark.GlobalResolver[object.LocalReference](func(identifier label.CanonicalStarlarkIdentifier) (model_core.Message[*model_starlark_pb.Value, object.LocalReference], error) {
		globals := encodedGlobals[identifier.GetCanonicalLabel()]
		return model_starlark.GetStructFieldValue(t.Context(), decodingOptions.Readers.List, model_core.Nested(globals, globals.Message.Globals), identifier.GetStarlarkIdentifier().String())
	}))
	thread.SetLocal(model_starlark.CompiledProgramResolverKey, model_starlark.CompiledProgramResolver[object.LocalReference](func(filename label.CanonicalLabel) (model_core.Message[*model_starlark_pb.CompiledProgram, object.LocalReference], error) {
		return encodedGlobals[filename], nil
	}))
	answer := eval.NewBuiltin("answer", func(*eval.Thread, *eval.Builtin, eval.Args) (eval.Value, error) {
		return eval.MakeInt(42), nil
	})

	t.Run("IdenticalFunctionsInDifferentFiles", func(t *testing.T) {
		v, err := eval.Call(thread, decodedGlobals[filenameA]["call"], eval.Args{
			Positional: []eval.Value{decodedGlobals[filenameB]["call"], answer},
		})
		require.NoError(t, err)
		require.Equal(t, eval.MakeInt(42), v)
	})

	t.Run("SameFunctionPassedAsArgument", func(t *testing.T) {
		_, err := eval.Call(thread, decodedGlobals[filenameA]["call"], eval.Args{
			Positional: []eval.Value{decodedGlobals[filenameA]["call"], answer},
		})
		require.ErrorContains(t, err, "function call called recursively")
	})

	t.Run("RecursionThroughGlobal", func(t *testing.T) {
		// The function obtained through the GlobalResolver is
		// a different object than the one that is called.
		_, err := eval.Call(thread, decodedGlobals[filenameA]["factorial"], eval.Args{
			Positional: []eval.Value{eval.MakeInt(5)},
		})
		require.ErrorContains(t, err, "function factorial called recursively")
	})
}

func TestFunctionNamePaths(t *testing.T) {
	filename := util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl"))
	encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
	store := &inMemoryObjectStore{objects: map[object.LocalReference]*object.Contents{}}
	encodingOptions := &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.NoopReferenceMetadata]{
		CurrentFilename:        &filename,
		Context:                t.Context(),
		ObjectEncoder:          encoder,
		ObjectReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
		ObjectCapturer:         store,
		ObjectMinimumSizeBytes: 256,
		ObjectMaximumSizeBytes: 1024,
	}

	// execAndEncode executes the provided source, and encodes the
	// resulting program.
	execAndEncode := func(t *testing.T, source string) (map[string]eval.Value, model_core.Message[*model_starlark_pb.CompiledProgram, object.LocalReference]) {
		f, err := syntax.Parse(filename.String(), []byte(source), syntax.Options{})
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
		module := model_starlark.NewBzlModule(filename, nil, nil)
		require.NoError(t, eval.ExecFile(newTestThread(t), program, module))
		encodedProgram, err := model_starlark.EncodeCompiledProgram(newTestThread(t), program, module.Globals(), encodingOptions)
		require.NoError(t, err)
		topLevel, _ := encodedProgram.SortAndSetReferences()
		return module.Globals(), topLevel.Decay()
	}
	namePath := func(t *testing.T, thread *eval.Thread, function eval.Value) []*model_starlark_pb.Function_Name {
		encoded, err := model_starlark.EncodeFunction(thread, function.(eval.Function), nil, encodingOptions)
		require.NoError(t, err)
		defer encoded.Discard()
		return encoded.Message.NamePath
	}
	call := func(t *testing.T, thread *eval.Thread, function eval.Value, args ...eval.Value) eval.Value {
		v, err := eval.Call(thread, function, eval.Args{Positional: args})
		require.NoError(t, err)
		return v
	}

	// Add enough functions, so that the B-tree containing the code
	// of functions consists of multiple levels.
	var source strings.Builder
	source.WriteString(`
def outer(x):
    if x:
        def g():
            return 1
    else:
        def g():
            return 2
    return g

def lambdas():
    return [lambda: 3, lambda: 4]

def deeply_nested():
    return lambda: lambda: 5

FIRST = lambda: "first"
SECOND = lambda: "second"
`)
	for i := range 200 {
		fmt.Fprintf(&source, "def f%d():\n    return %d\n", i, i)
	}
	globals, program := execAndEncode(t, source.String())
	require.NotNil(t, program.Message.Functions[0].GetParent())

	name := func(name string, index int32) *model_starlark_pb.Function_Name {
		return &model_starlark_pb.Function_Name{Name: name, DisambiguationIndex: index}
	}
	requireNamePath := func(t *testing.T, thread *eval.Thread, function eval.Value, expected ...*model_starlark_pb.Function_Name) {
		actual := namePath(t, thread, function)
		require.Len(t, actual, len(expected))
		for i := range expected {
			testutil.RequireEqualProto(t, expected[i], actual[i])
		}
	}

	t.Run("InMemory", func(t *testing.T) {
		thread := newTestThread(t)
		requireNamePath(t, thread, globals["outer"], name("outer", 0))
		requireNamePath(t, thread, globals["FIRST"], name("", 0))
		requireNamePath(t, thread, globals["SECOND"], name("", 1))
		requireNamePath(t, thread, call(t, thread, globals["outer"], eval.True), name("outer", 0), name("g", 0))
		requireNamePath(t, thread, call(t, thread, globals["outer"], eval.False), name("outer", 0), name("g", 1))
		requireNamePath(t, thread, call(t, thread, call(t, thread, globals["deeply_nested"])), name("deeply_nested", 0), name("", 0), name("", 0))
	})

	t.Run("Stored", func(t *testing.T) {
		thread := newTestThread(t)
		thread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{
			Readers: &model_starlark.ValueReaders[object.LocalReference]{
				CompiledProgramFunctions: newProtoListReader[model_starlark_pb.CompiledProgram_FunctionEntry](store, encoder),
				List:                     newProtoListReader[model_starlark_pb.List_Element](store, encoder),
			},
		})
		thread.SetLocal(model_starlark.CompiledProgramResolverKey, model_starlark.CompiledProgramResolver[object.LocalReference](func(label.CanonicalLabel) (model_core.Message[*model_starlark_pb.CompiledProgram, object.LocalReference], error) {
			return program, nil
		}))
		decoded, err := model_starlark.DecodeGlobals[object.LocalReference, model_core.NoopReferenceMetadata](thread, model_core.Nested(program, program.Message.Globals), filename)
		require.NoError(t, err)

		// Functions should resolve to the right code, regardless
		// of where they are stored in the B-tree.
		require.Equal(t, eval.MakeInt(0), call(t, thread, decoded["f0"]))
		require.Equal(t, eval.MakeInt(199), call(t, thread, decoded["f199"]))
		require.Equal(t, eval.String("first"), call(t, thread, decoded["FIRST"]))
		require.Equal(t, eval.String("second"), call(t, thread, decoded["SECOND"]))

		// Functions created from code that was loaded from
		// storage should get correct name paths as well, which
		// should in turn resolve to the right code once decoded.
		roundTrip := func(t *testing.T, function eval.Value) eval.Value {
			encoded, err := model_starlark.EncodeFunction(thread, function.(eval.Function), nil, encodingOptions)
			require.NoError(t, err)
			topLevel, _ := encoded.SortAndSetReferences()
			return model_starlark.DecodeFunction[object.LocalReference, model_core.NoopReferenceMetadata](topLevel.Decay())
		}
		for _, test := range []struct {
			argument eval.Value
			expected eval.Value
		}{
			{eval.True, eval.MakeInt(1)},
			{eval.False, eval.MakeInt(2)},
		} {
			g := call(t, thread, decoded["outer"], test.argument)
			require.Equal(t, test.expected, call(t, thread, roundTrip(t, g)))
		}
		lambda := call(t, thread, call(t, thread, decoded["deeply_nested"]))
		requireNamePath(t, thread, lambda, name("deeply_nested", 0), name("", 0), name("", 0))
		require.Equal(t, eval.MakeInt(5), call(t, thread, roundTrip(t, lambda)))
		lambdas := call(t, thread, decoded["lambdas"]).(eval.List)
		for i, expected := range []eval.Value{eval.MakeInt(3), eval.MakeInt(4)} {
			element, err := lambdas.Index(thread, i)
			require.NoError(t, err)
			require.Equal(t, expected, call(t, thread, roundTrip(t, element)))
		}
	})

	t.Run("BodyChangesDoNotAffectFunctionMessage", func(t *testing.T) {
		// Functions refer to their code by name, so values that
		// embed a function should not change if its body does.
		encode := func(t *testing.T, source string) (*model_starlark_pb.Function, []*model_starlark_pb.CompiledProgram_FunctionEntry) {
			globals, program := execAndEncode(t, source)
			encoded, err := model_starlark.EncodeFunction(newTestThread(t), globals["f"].(eval.Function), nil, encodingOptions)
			require.NoError(t, err)
			defer encoded.Discard()
			return encoded.Message, program.Message.Functions
		}
		function1, code1 := encode(t, "def f():\n    return 1\n")
		function2, code2 := encode(t, "def f():\n    return 2\n")
		testutil.RequireEqualProto(t, function1, function2)
		require.False(t, proto.Equal(code1[0], code2[0]))
	})
}
