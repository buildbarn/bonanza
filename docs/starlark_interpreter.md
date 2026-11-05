# Design: a Starlark interpreter for Bonanza

Status: proposal

## Motivation

Bonanza currently evaluates `.bzl`, `BUILD`, `MODULE.bazel` and
`REPO.bazel` files using a patched copy of `go.starlark.net`. This has
the following problems:

1. **Patch burden.** `patches/net_starlark_go/` contains ~2,900 lines of
   diffs. `thread.diff` alone (~2,450 lines) threads `*Thread` through
   `Attr()`, `Hash()`, `Binary()`, `CompareSameType()`, `SetKey()`,
   etc. `function-saving.diff` adds a `FunctionFactory` so functions can
   be reconstructed by bytecode index. These are core API changes that
   will never be accepted upstream.
2. **One implementation per composite type.** `*starlark.List`,
   `*starlark.Dict`, `starlark.Tuple` and `*starlark.Set` are concrete
   types that the interpreter type-switches on. A list can't be backed
   by a B-tree in storage, so `DecodeValue()` reads whole
   `model_starlark_pb.List` and `Dict` trees into memory, which
   duplicates data that is already cached as parsed objects.
3. **Methods that can't fail or take a context.** `String()` has no
   `*Thread`, so `Struct.String()` prints `field = ...` instead of
   decoding fields from storage.
4. **Eager `load()`.** `Thread.Load` must return a complete
   `StringDict`, so `CompiledBzlFileDecodedGlobals` decodes every
   global of every loaded file, even when only one symbol is used.
5. **Native values in the evaluation graph.**
   `CompiledBzlFileDecodedGlobals` (`starlark.StringDict`) and
   `CompiledBzlFileFunctionFactory` (`*starlark.FunctionFactory`) are
   the Starlark-specific reasons `pkg/model/evaluation` has to support
   `ComputeNativeValue()`. Even worse, building a `FunctionFactory`
   re-executes the top-level code of a `.bzl` file whenever a function
   declared in it gets called from another evaluation.

## Goals

- Own the whole pipeline, from the scanner down: no dependency on
  `go.starlark.net` and no patches.
- Follow Bazel's Starlark syntax as it evolves, starting with type
  annotations.
- Make composite types interfaces with multiple implementations: in
  memory, or backed by storage, lazy and immutable.
- Make every operation that may touch storage take a `*Thread` (which
  carries a `context.Context`) and return an `error`, including
  `repr()`/`str()`.
- Make `load()` and global access lazy, per symbol.
- Make functions plain message values that can be called without
  re-running the top-level code of their file, so that the
  Starlark-related native value keys can be removed.
- Stay compatible with the Bazel Starlark dialect that
  `go.starlark.net` + patches implements today, including set
  literals, octal escapes and `dict.get(key=..., default=...)`.

## Non-goals (for now)

- Avoiding restarts when an `ErrMissingDependency` occurs. The design
  below must not rule this out, though (see
  [Future work](#future-work)).
- Concatenating lists, strings or bytes without reading them. This
  needs canonical concatenation in `pkg/model/core/btree` first (see
  [Future work](#future-work)).
- A static type checker. Type annotations are parsed, and get the
  run-time behaviour the spec gives them, but no types are checked (see
  [Type annotations](#type-annotations)).
- JIT compilation or other performance work beyond a reasonable
  bytecode VM.

## Overview

```
pkg/starlark/syntax     scanner, parser, AST, quoting  (new)
pkg/starlark/resolve    scope resolution, dialects     (new)
pkg/starlark/compile    AST -> bytecode, Funcode proto (new)
pkg/starlark/eval       Value interfaces, core types,
                        Thread, VM, error/backtrace,
                        builtins, methods, json        (new)
pkg/starlark/unpack
                        argument unpacking             (ported)
pkg/model/starlark      storage-backed implementations,
                        Bazel types (Label, depset,
                        struct, rule, ...)             (existing, ported)
```

The core interpreter lives in `pkg/starlark/eval`, next to the
packages implementing the other stages of the pipeline. The unpackers
for builtin arguments live in `pkg/starlark/unpack`, and the
`MODULE.bazel` parser lives in `pkg/moduledotbazel`. The core knows nothing about the object store and has no
`[TReference, TMetadata]` generics. Storage-backed
values are implementations of the core interfaces that live in
`pkg/model/starlark`, just like `Struct` already does today.

### Why write our own scanner and parser

Upstream `syntax` isn't involved in the problems listed under
Motivation. We write our own anyway, because Bazel's syntax is moving
and go.starlark.net isn't keeping up:

- **Type annotations.** Bazel's spec change
  ([bazelbuild/starlark#362](https://github.com/bazelbuild/starlark/pull/362))
  adds annotation syntax: annotations on parameters (including
  `*args`/`**kwargs`) and return types, typed assignments, type
  expressions such as `list[str]` and namespaced names, and `cast()`
  and `isinstance()` as keywords. bazel-contrib/buildtools already
  supports it, and Bazel 8.8 parses and ignores it behind
  `--experimental_starlark_type_syntax`. The only go.starlark.net
  proposal
  ([google/starlark-go#439](https://github.com/google/starlark-go/pull/439))
  covers function annotations only, and has been open since 2022. Once
  modules on the Bazel Central Registry use annotations, Bonanza has
  to parse them. Blanking out annotations before parsing doesn't work,
  because `isinstance(value, type)` has to evaluate at run time.
- **No patches.** Today's `octal.diff` is needed because upstream
  rejects `"\377"`-style escapes while scanning, before any of our code
  runs. Our own scanner follows Bazel's lexer instead:
  - An octal escape is a single byte, even when that makes the string
    invalid UTF-8.
  - `\x`, `\u`, `\U` and `\N` escapes are rejected.
  - Bytes literals (`b"..."`) don't exist.
  - Identifiers are ASCII only.
  - Tabs may not be used for indentation outside brackets.
  - Carriage returns are ignored outside string literals.
  - Words such as `class` and `import` are reserved.
  - Nesting and the depth of the syntax tree are limited, as files may
    otherwise cause a stack overflow, which terminates the process.
    Chains such as `a + b + c` are parsed iteratively, but also yield
    deep trees that later stages process recursively.
- **An AST that fits our resolver.** Upstream's AST holds resolver
  output in `interface{}` slots (`File.Module`, `DefStmt.Function`,
  `Ident.Binding`). Ours keeps no resolver state at all (see
  [Resolver](#resolver)), and has nodes for type expressions.
- **We decide which syntax to accept.** Each dialect has an option for
  annotations, so we can follow Bazel's spec as it changes, rather
  than go.starlark.net's.

It isn't much code: upstream `syntax` is ~3,250 lines without tests,
mostly the scanner and parser (~1,100 lines each). The grammar is
small and well specified. We keep the AST shape close to upstream's
where that's reasonable, and port go.starlark.net's scanner and parser
tests (BSD-3-Clause).

We considered bazel-contrib/buildtools' `build` package, which does
track Bazel's syntax closely. But it's written for buildifier, a
formatter: it keeps comments, accepts more than is valid, and its AST
suits rewriting files, not compiling them.

### Type annotations

Parsing follows the type-expression grammar of bazelbuild/starlark#362,
taking the union of what Bazel's Java parser and bazel-contrib/buildtools
accept where the two differ. Type expressions reuse the `Expr` nodes
where possible, parsed in a restricted mode. Type applications such as
`list[str]` get a dedicated `TypeApplication` node, so that they can't
be confused with indexing. Parameters, `DefStmt` and assignments get an
optional type field, and bare declarations (`x: int`) become a
`VarStmt`. Support comes in two steps:

1. **Parse and ignore,** as Bazel 8.8 does. Annotations are kept in the
   AST and resolved like other names, which catches undefined type
   names. `cast(x, T)` evaluates to `x`.
2. **Run-time behaviour,** such as `isinstance(value, type)`, once the
   spec has settled. It is implemented in the compiler and in
   `pkg/starlark/eval`.

Annotation syntax is enabled through `syntax.Options.TypeSyntax`, off
by default until the spec is merged. It has to be a parser option
rather than a resolver check, because it turns `cast` and `isinstance`
into keywords. Checking types stays a non-goal.

### Why write our own resolver

Upstream `resolve` gets in the way:

- It treats `load()` bindings as locals of the file. A function that
  uses a loaded symbol then captures it as a free variable, which
  defeats [lazy `load()`](#lazy-load).
- Its dialect options are global flags, linknamed from
  `syntax.FileOptions`. They can't express Bazel's per-file-type
  restrictions.
- Its output (`resolve.Binding`, `resolve.Function`) has no room for
  the extra information our compiler needs.

As in upstream, our parser accepts the full grammar and leaves all
dialect checks (e.g. top-level `if`/`for`, `set`, global reassignment)
to the resolver. The exceptions are `while` statements, which no
dialect permits and are rejected by the parser, and type syntax (see
[Type annotations](#type-annotations)). Everything from the
compiler down is affected by the value model, so it is new code as
well.

### Resolver

`pkg/starlark/resolve` takes a `*syntax.File` and a `Dialect`. It
doesn't write anything into the AST. Instead it returns a `*Module`,
which maps AST nodes to its results (`Module.Binding(*syntax.Ident)`
and `Module.Function(node)` for a `*syntax.DefStmt` or
`*syntax.LambdaExpr`). This keeps the AST immutable, so other packages
such as `pkg/model/render` can use it without caring about resolution:

```go
type Scope uint8

const (
	Undefined     Scope = iota // could not be resolved; an error is reported
	Local                      // local to its function or comprehension
	Cell                       // local, but captured by a nested function
	Free                       // captured from an enclosing function
	Global                     // defined at the top level of this module
	Import                     // bound by load(); resolved lazily
	Predeclared                // e.g. rule(), glob(), native
	Universal                  // e.g. len(), str()
	TypeParameter              // e.g. T in "def f[T](x: T)"
)

type Binding struct {
	Scope Scope
	Index int // into Function.Locals/FreeVars, Module.Globals or Module.Imports
	First *syntax.Ident
}

type LoadedSymbol struct {
	Module  string   // as written; the caller canonicalizes it
	Name    string   // name in the loaded module
	Binding *Binding
}

type Module struct {
	Globals  []*Binding
	Imports  []*LoadedSymbol
	Toplevel *Function // its Locals are top-level comprehension variables
}

type Function struct {
	Name     string
	Node     syntax.Node // *DefStmt or *LambdaExpr; nil for the top level
	Params   []*syntax.Param
	// Parameters first, in runtime order: positional, keyword-only,
	// *args, **kwargs.
	Locals   []*Binding
	FreeVars []*Binding // bindings in the directly enclosing function
	NumKeywordOnlyParams int
	HasArgs, HasKwargs   bool
	// Globals and imports referenced by this function or any
	// function nested inside it, so that the compiler knows which
	// private globals have to be encoded.
	ReferencedGlobals, ReferencedImports []int
}
```

`Import` is a scope of its own, not a captured local. When a function
references a loaded symbol, the compiler emits an import reference to
that symbol, not a free variable. The resolver keeps the module string
of each `load()` as written. Turning it into a canonical label needs
the repo mapping, so that is up to the caller.

Name resolution follows Bazel's resolver. Before the names used in a
block are resolved, all bindings in that block are created: the top
level of the file, a function body, or all loop variables of a
comprehension. A name that is assigned anywhere at the top level
therefore refers to that global everywhere in the file, even before
the assignment and even if a predeclared name of the same name exists.
Because all enclosing blocks are complete by the time a use is visited,
each use is resolved immediately. Unlike go.starlark.net, nothing has
to be deferred to the end of the file.

`Dialect` replaces upstream's linknamed `syntax.FileOptions` flags. It
contains the options of Bazel's `FileOptions` that matter here
(`AllowToplevelRebinding`,
`RequireLoadStatementsFirst`), plus `ResolveTypeSyntax` and an optional
`RestrictedSyntax`.
The latter mirrors Bazel's `DotBazelFileSyntaxChecker`, including its
error messages. There is one preset per file type, based on Bazel's
`BzlCompileFunction`, `PackageFunction` and `CompiledModuleFile`:

| | `.bzl` | `BUILD` | `MODULE.bazel` | `REPO.bazel` |
|---|---|---|---|---|
| `def` / `lambda`, `if` / `for` | inside functions | ✗ | ✗ | ✗ |
| `*args` in calls | ✓ | ✗ | ✗ | ✗ |
| `**kwargs` in calls | ✓ | ✗ | dict literal only | dict literal only |
| `load()` | before other statements | anywhere | ✗ | ✗ |
| reassigning top-level names | ✗ | ✓ | ✗ | ✗ |
| loading `_private` symbols | ✗ | ✗ | — | — |

`if` and `for` are never permitted at the top level, in any file type.
Symbols bound by `load()` are file-local and loaded lazily in all file
types. Unlike Bazel, a name in a `BUILD` file can't be both loaded and
assigned, or loaded as two different symbols, as nothing reads the
globals of `BUILD` files that would make this distinction useful.
The `REPO.bazel` column is a best guess, as Bazel's `RepoFileFunction`
couldn't be checked. Recursion is not a resolver concern; it is
detected when calls are made. `MODULE.bazel`'s rules for `include()`
are left to the code that evaluates `MODULE.bazel` files. Type
annotations are only resolved if `ResolveTypeSyntax` is set. Otherwise
they are ignored, and type alias statements bind no names. As in Bazel,
`isinstance()` is parsed but rejected by the resolver for now. Only
symbols defined in a module are exported, not loaded ones, which
matches Bazel. Undefined names get "did you mean …?" suggestions from a
small spelling helper of our own, because upstream's `internal/spell`
can't be imported.

The tests include upstream's `resolve/testdata` cases (BSD-3-Clause),
adapted to Bazel's semantics and error messages. A second test resolves
a corpus of real files. It checks that they resolve without errors,
and that the scope of every identifier in `.bzl` files matches what
go.starlark.net's resolver computes, apart from loaded symbols.

## Value model

### Base interface

```go
package starlark

type Value interface {
	// Type returns the name reported by type(). It never needs storage.
	Type() string

	// Truth implements bool(x). For a storage-backed list this needs
	// only the root node, since parent elements carry counts.
	Truth(t *Thread) (bool, error)

	// Repr appends repr(x) to the printer. Used by repr(), str(),
	// print(), "%r"/"%s", and error messages. It may load data.
	Repr(t *Thread, p *Printer) error
}
```

There is no `Freeze()` method. A value is either mutable or immutable
from the moment it is created (see [Mutability](#mutability)).

Optional capabilities are separate interfaces, as in `go.starlark.net`,
but all of them take a `*Thread` and can fail:

```go
type Hashable   interface { Value; Hash(t *Thread) (uint32, error) }
type Comparable interface { Value; CompareSameType(t *Thread, op syntax.Token, y Value, depth int) (bool, error) }
type HasAttrs   interface { Value; Attr(t *Thread, name string) (Value, error); AttrNames(t *Thread) ([]string, error) }
type HasBinary  interface { Value; Binary(t *Thread, op syntax.Token, y Value, side Side) (Value, error) }
type Indexable  interface { Value; Len(t *Thread) (int, error); Index(t *Thread, i int) (Value, error) }
type Iterable   interface { Value; Iterate(t *Thread, errOut *error) iter.Seq[Value] }
type Mapping    interface { Value; Get(t *Thread, k Value) (Value, bool, error) }
type Callable   interface { Value; Name() string; CallInternal(t *Thread, args Args) (Value, error) }
```

`Iterate` follows the `errOut *error` + `iter.Seq` style already used
by `btree.AllLeaves()` and `AllListLeafElements()`.

`Attr` reports a missing attribute by returning `NoSuchAttrError`, not
`(nil, nil)`. `getattr(x, name, default)` and `hasattr()` only fall
back to the default for that error, and propagate all others (including
`ErrMissingDependency`). This replaces `getattr-error-checking.diff`.

### Composite kinds are interfaces

The VM and library never type-switch on concrete composite types.
Instead each kind is an interface:

```go
type List interface {
	Indexable
	Iterable
	isList()
	// Mutation methods return an error on immutable lists.
	Append(t *Thread, v Value) error
	SetIndex(t *Thread, i int, v Value) error
	// ...
}

type Dict interface { Mapping; Iterable; isDict(); Len(t *Thread) (int, error); SetKey(t *Thread, k, v Value) error; Items(t *Thread, errOut *error) iter.Seq2[Value, Value]; ... }
type Tuple interface { Indexable; Iterable; isTuple() }
type Set   interface { Iterable; isSet(); Has(t *Thread, v Value) (bool, error); ... }
```

The unexported marker methods keep `type(x) == "list"` meaningful: any
`List` implementation reports `"list"`, and arbitrary types can't
pretend to be lists by accident.

Implementations:

| Kind | In-memory (`pkg/starlark/eval`) | Storage-backed (`pkg/model/starlark`) |
|---|---|---|
| list | `*memList`: `[]Value`, mutable unless created immutable | `*storedList`: root `[]*List_Element`, decodes nodes on demand through the parsed object pool |
| dict | `*memDict`: insertion-ordered hash table | `*storedDict`: entries sorted by key, plus insertion order, see [Stored dicts and sets](#stored-dicts-and-sets) |
| tuple | `memTuple` | `storedTuple` |
| set | `*memSet` | `*storedSet`: same layout as dicts, without values |
| struct / provider instance | — | `*Struct` (already lazy per field) |
| depset | — | `*Depset` (already list-backed) |

The VM has fast paths for the in-memory implementations (`if l, ok :=
x.(*memList)`) and falls back to the interface otherwise. That keeps
the common case of locally built lists cheap.

Scalars stay as concrete value types: `NoneType`, `Bool`, `Int` (int64
fast path, `*big.Int` for overflow), `Float`, `String`, `Bytes`.
Strings are Go byte strings. That matches how Bazel behaves in
practice, since Bazel reads source files as Latin-1, so a "char" in
Bazel is a byte. Like Bazel, we allow strings that aren't valid UTF-8,
and an escape like `"\377"` is the single byte 0xFF. (The current
`octal.diff` instead writes U+00FF as UTF-8, i.e. the two bytes C3 BF,
which is not what Bazel does.) Strings and bytes that are too large to store inline
have storage-backed implementations as well (see
[Large strings and bytes](#large-strings-and-bytes)).

### Storage-backed values

A storage-backed value wraps a
`model_core.Message[*model_starlark_pb.X, TReference]` and decodes as
little as possible:

- `len()` on a stored list sums the counts of the root's elements
  without reading children. `l[i]` descends the B-tree using the
  counts.
- Iteration reads nodes one at a time. Prefetching is left for later
  (see [Future work](#future-work)).
- Element values are decoded when they're accessed. Decoding a nested
  list/dict/struct yields another storage-backed value, never a deep
  copy.
- Parsed nodes are shared through `model_parser`'s
  `ParsedObjectPool`. The per-thread `Value` wrappers point into them,
  so no second copy of the data exists.

Storage-backed values are immutable. The `Value` objects that wrap
them still belong to a single `Thread`, though (see
[Memoization and dependency tracking](#memoization-and-dependency-tracking)).
What is shared between evaluations is the parsed objects underneath.

`DecodeEvalValue()` in `pkg/model/starlark` decodes `Value` messages
this way. Types outside `pkg/starlark/eval` mark themselves as lists,
tuples, dicts or sets by embedding `eval.ListMarker`,
`eval.TupleMarker`, `eval.DictMarker` or `eval.SetMarker`. Values that
implement `EncodableEvalValue` provide their own encoding, which
`EncodeEvalValue()` uses instead of walking their contents.

Dicts and sets currently use the existing storage format, which stores
them in insertion order. That format has to stay in place until the
go.starlark.net based code that reads and writes the same messages is
gone. A stored set can compute its length from the root, as its
parents carry counts. Everything else that needs random access, such as
`len()` of a dict, `d[k]`, `x in s` and equality, builds an in-memory
copy on first use. That copy belongs to the wrapper, and so to the
thread. Switching to the sorted layout described in
[Stored dicts and sets](#stored-dicts-and-sets) removes the need for
it. Tuples are stored inline, so they decode to in-memory tuples
whose elements may in turn be storage-backed.

### Memoization and dependency tracking

An evaluation records which other keys it depends on through the
lookups it makes on its `Environment`. A value that memoizes the
result of such a lookup, and is shared with another evaluation, hides
that lookup from the second evaluation. The second evaluation then
loses that dependency edge, or not, depending on which evaluation ran
first. That makes the dependency graph unstable. Today this happens
in three places, two of which are already marked with a `TODO`:

- `protoNamedFunctionDefinition.getFunction()`
  (`named_function.go`) caches the `*starlark.Function` it obtained
  through `CompiledBzlFileFunctionFactory`;
- `reloadingRuleDefinition.getBase()` (`rule.go`) caches the rule
  definition it obtained through the `GlobalResolver`;
- `reloadingMacroDefinition.getBase()` (`macro.go`) does the same for
  macros.

They can be shared because they are reachable from native values
(`CompiledBzlFileDecodedGlobals`, `CompiledBzlFileFunctionFactory`)
that the `RecursiveComputer` keeps around. The new design avoids this
in general, not just for these three:

1. **Only results that depend purely on storage contents are shared
   between evaluations.** These are parsed objects in the
   `ParsedObjectPool`, keyed by reference, and things derived from
   them alone, such as a parsed `Funcode`, the attrs cache of
   `protoAttrsCache`, or a decoded B-tree node. Reading objects
   reachable from a value an evaluation already depends on doesn't
   need an extra dependency edge, just like reading the children of a
   B-tree.
2. **Anything obtained through the `Environment` is looked up again in
   every evaluation.** Examples are a global resolved through
   `CompiledBzlFileGlobal`, an import, or the definition behind a rule
   or macro that is referenced by identifier. Within one evaluation it
   may be cached on the `Thread`. Within a single evaluation that's
   harmless, because the edge has been recorded on first use, and the
   `Environment` already deduplicates repeated lookups.
3. **`Value` objects never outlive their `Thread`.** Storage-backed
   values are created per thread, as cheap wrappers around parsed
   messages. Values cross evaluation boundaries only as messages (see
   [Mutability](#mutability)), and the Starlark-related native value
   keys are gone. So there is no shared `Value` object that something
   could be memoized into, and per-value memoization such as
   `Struct.decodedValues` is per-evaluation automatically.
4. **Shared state never captures per-evaluation state.** That includes
   the `Thread`, `context.Context`, `Environment` and encoding/decoding
   options. Today `Struct.fieldAtIndex()` decodes with the options of
   whichever thread first accessed the field. Under rule 3 that is
   always the current thread.

The function design follows these rules. A `Function` message refers to
its code by name, and every evaluation that calls it looks up the code
in the `CompiledProgram` of its file. The module's globals and imports
are resolved through `CompiledBzlFileGlobal` by every evaluation that
uses them. Rules and macros that are referenced by identifier become
storage-backed values that resolve their definition per thread, the
same way.

To keep this from regressing, `pkg/model/evaluation` gets a test mode
that evaluates the same keys in different orders, and with cold and
warm caches. It then checks that every key ends up with the same set
of dependencies. This can sit alongside the existing leak-checking
computer.

### Mutability

In `go.starlark.net`, values start out mutable and are frozen later by
`Freeze()`, which walks everything reachable from them. That is how
the globals of a module become immutable once it has been loaded, and
how values can be shared between threads safely. Here, values never
cross an evaluation boundary in memory. They are always written to
storage and read back as storage-backed values, which have no way to
mutate. So the state change, and the graph walk that comes with it,
aren't needed. The rules:

1. **Mutability is fixed when a value is created; it never changes
   later.** Storage-backed values are immutable. In-memory values are
   mutable, unless Go code creates them with an immutable constructor.
2. **Decoding never produces a mutable value.** Decoding a nested
   list, dict or struct yields another storage-backed value.
3. **Values cross Thread and evaluation boundaries only by being
   encoded.** In-memory values are confined to the thread that created
   them and need no locking. A compute function that runs more than one
   Starlark thread doesn't share in-memory values between them.
4. **Builtins that keep a reference to an argument see later
   mutations**, e.g. `s = struct(x = L)` followed by `L.append(...)`
   changes `s.x`. That matches Starlark semantics. The value is encoded
   in its final state when it leaves the thread, which is also what
   Bazel ends up with.
5. **Mutating a container while iterating over it is an error.** This
   check is a separate mechanism, as it is in `go.starlark.net`.

This matches Bazel's behaviour. While a `.bzl` file's top-level code
runs, its globals and the default values of its functions are mutable
in memory. Once another evaluation loads the file, they are
storage-backed and immutable, so `FOO.append(1)` fails with the same
"frozen list" error as before.

**Immutable values created by Go code.** Some values are built by Go
code and handed to Starlark within the same evaluation, and Starlark
must not be able to mutate them. Today these are made immutable by
calling `Freeze()`:

- `ctx.attr` values, built by concatenating decoded parts
  (`configured_target.go`);
- split attributes and `ctx.files` lists (`configured_target.go`);
- the `settings` dict passed to transitions
  (`user_defined_transition.go`);
- the result of `native.existing_rules()` (`existing_rules.go`);
- `module_ctx` (`module_extension_repos.go`).

Writing these to storage just to read them back would be a wasteful
round trip, especially for `ctx.files`. Instead, the core provides
immutable constructors for the in-memory implementations:

```go
func NewImmutableList(elements []Value) List
func NewImmutableDict(entries ...KeyValue) Dict
func NewImmutableTuple(elements []Value) Tuple
```

An immutable in-memory value is only safe if its elements are
immutable too. That already holds at the call sites above, because
they assemble decoded values, labels and files. In debug builds the
constructors check this with `IsImmutable(v Value) bool`.

What follows from this:

- The `frozen` tracking in `Struct`, `Select` and other types in
  `pkg/model/starlark` goes away. So does the freezing of decoded
  values in `Struct.fieldAtIndex` and `DecodeGlobals`.
- Whether a value is hashable never depends on whether it is mutable.
  As in `go.starlark.net` today, lists, dicts and sets are never
  hashable, whatever their implementation.

### Encoding is structural sharing

Every value that leaves a thread has to be encodable, because values
returned from an evaluation are messages. The encode interface stays close to
today's `EncodableValue`:

```go
type EncodableValue[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	EncodeValue(t *Thread, path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error)
}
```

A storage-backed value encodes by returning its original message,
patched through `options.ObjectCapturer` (as
`protoNamedFunctionDefinition.Encode()` already does), **without
reading any children**. So passing a large provider field from a
dependency through to your own providers costs O(1) instead of a full
decode/re-encode round trip.

The `needsCode` return value goes away (see [Functions](#functions)).

### Equality and hashing

`Equal(t, x, y)` and `Compare(t, op, x, y)` live in the core. Values of
the same kind compare by content regardless of implementation (a
`*memList` equals a `*storedList` with the same elements). Two
storage-backed values pointing at the same object (same reference and
same message within it) are equal without being read. This makes
comparing unchanged providers or attribute values cheap.

There are two separate hash functions, with different requirements:

1. **The internal hash used by dicts and sets** (`Hashable.Hash()`).
   Starlark code can't observe it: dicts and sets iterate in insertion
   order, and stored dicts and sets are ordered by
   [key ordering](#key-ordering), not by hash. So it may differ between
   processes and change between releases. Use
   `hash/maphash` with a process-wide seed for strings, and cache the
   hash of immutable composite values (e.g. `Struct.Hash()`, which
   today assumes that any hashable struct is immutable).
2. **The `hash()` builtin.** Its result is visible to Starlark code,
   which may store it in a global or a provider, so it ends up in
   storage. It must therefore be deterministic and never change. It is
   a fixed function, implemented independently of `Hashable.Hash()`.
   As in the Starlark spec and Bazel, it accepts only strings and uses
   Java's `String.hashCode()`. (go.starlark.net also accepts bytes,
   hashed with FNV-32.)

   go.starlark.net transcodes the string to UTF-16 before hashing.
   Bazel stores strings internally as Latin-1 characters, one per byte
   of the file, so it effectively hashes the bytes. The two only agree
   on ASCII strings. We hash the bytes, as Bazel does. That also fits
   strings not having to be valid UTF-8 (see
   [Composite kinds](#composite-kinds-are-interfaces)). This choice is
   permanent once values are in storage.

### Stored dicts and sets

Today `Dict.Entry` is stored in insertion order, so a stored dict has
no index for `d[k]`. Instead, entries are stored sorted by key, with
the insertion order stored next to them:

```proto
message Dict {
  message Entry {
    message Leaf {
      Value key = 1;
      Value value = 2;
    }

    message Parent {
      bonanza.model.core.DecodableReference reference = 1;
      // Number of entries in the subtree, so that the N-th entry can
      // be found without reading other subtrees.
      uint64 count = 2;
      // Smallest key in the subtree, so that lookups can descend
      // without reading other subtrees.
      Value first_key = 3;
    }

    oneof level {
      Leaf leaf = 1;
      Parent parent = 2;
    }
  }

  // Entries, sorted by key ordering.
  repeated Entry entries = 1;

  // Insertion order, as positions into `entries`. Left out if the
  // insertion order equals the sorted order.
  repeated InsertionOrder.Element insertion_order = 2;
}

message InsertionOrder {
  message Element {
    message Parent {
      bonanza.model.core.DecodableReference reference = 1;
      uint64 count = 2;
    }

    oneof level {
      // Positions of consecutive entries, as packed varints.
      Positions leaf = 1;
      Parent parent = 2;
    }
  }
}
```

`Set` uses the same layout without values: its elements are sorted, and
it has an optional `insertion_order`.

What this gets us:

- **Lookups.** `d[k]`, `k in d` and `d.get(k)` take O(log n) object
  reads by descending through `first_key`. No in-memory index has to be
  built first.
- **Equality ignores order.** In Starlark, `{a: 1, b: 2} == {b: 2, a:
  1}` is true. Comparing two stored dicts or sets is a walk over both
  sorted lists, and subtrees with the same reference are skipped
  without being read.
- **Better deduplication.** Prolly chunking is content-defined, so
  dicts with the same contents share their sorted subtrees whatever
  their insertion order. A dict with a few changed entries shares most
  of its objects with the original.
- **Merge-based operations.** `d1 | d2`, `set.union()`,
  `intersection()`, `difference()`, `isdisjoint()` and `issubset()`
  become linear walks over both sorted inputs.
- **No insertion-order list in the common case.** It is left out when
  insertion order already equals sorted order, which should be common
  for dicts built by Go code or from sorted inputs. This is why entries
  are sorted by key and not by hash: with hash order, insertion order
  would essentially never equal sorted order.

What it costs:

- **Insertion-order iteration jumps around.** Each step looks up the
  next position, at O(log n). Every node is still read only once as
  long as the parsed object pool holds all of them, so this mainly hurts
  dicts that are larger than the cache. Code that doesn't care about
  order walks the sorted entries directly. Examples are equality,
  `len()`, set operations and the `in` operator.
- **Encoding sorts.** Turning an in-memory dict or set into a stored
  one takes O(n log n).
- **Modified copies rewrite the insertion-order list.** If a copy of a
  stored dict gets a key inserted in the middle, positions shift and
  the whole list is rewritten. That is a few bytes per entry. The
  sorted entries still mostly deduplicate.
- **Parent nodes repeat a key.** Each parent stores its child's first
  key. Keys are almost always short strings or labels.

### Key ordering

Stored dicts and sets are sorted with a fixed total order over all
hashable values. Like `hash()`, this ordering is part of the storage
format and can never change once dicts are in storage. It is internal
only: `sorted()`, `min()`, `max()` and `<` keep Starlark's semantics,
including errors on mixed types.

1. **Rank by type first.** `None` < `bool` < numbers < `str` < `bytes`
   < `tuple` < `Label` < `struct` < other hashable types, in an order
   fixed by the implementation.
2. **Then compare values of the same type.** Numbers compare by value.
   Strings and bytes compare by bytes. Tuples compare element by
   element. Labels compare by their canonical string form. Structs
   compare by their sorted field names, then their field values.
3. **Consistent with `==`.** Two keys are equal in this ordering
   exactly when Starlark considers them equal. For example, `1 == 1.0`
   is true, so ints and floats share one rank and are ordered by
   numeric value.
4. **Based on content, never on identity.** Functions order by their
   message (filename, name path and closure). Depsets and other
   reference-equal values order by their reference-equal identifier.

Tuples are stored inline (`repeated Value elements`), so comparing
tuple keys never reads storage. Only struct keys with fields stored
out of line might. That is why the comparison takes a `*Thread` and
can fail:

```go
func CompareKeys(t *Thread, x, y Value) (int, error)
```

### Large strings and bytes

`Value.str` and `Value.bytes` used to be proto3 `string` and `bytes`
fields, always stored inline. That puts a lower bound on the maximum
object size, and forces arbitrary limits on reads, such as the 4 MiB
limit on `stdout` in `repo.go`. It also means that strings that are
not valid UTF-8 can't be stored, such as the octal escapes in
`@bazel_lib//lib/private:strings.bzl`, as the Go protobuf library
refuses to marshal them. We
want to experiment with smaller maximum object sizes, so large strings
and bytes are stored as trees as well. Rather than adding yet another
B-tree type, we reuse the encoding that `pkg/model/filesystem` already
uses for file contents.

Data that is usually small but may be large comes up in other places
too, e.g. the `stdout`/`stderr` logs of commands (`command.Log`), the
contents of `MODULE.bazel` files and downloaded files. So instead of a
Starlark-specific pair of fields, `bonanza.model.filesystem` gets a
reusable message. It follows the existing `*_inline`/`*_external`
naming of `DirectoryContents` and `Directory`:

```proto
// Data that is stored inline if it is small enough, and as file
// contents in separate objects otherwise. Empty data is represented by
// leaving the oneof unset, like FileContents fields elsewhere.
message MaybeInlineFileContents {
  oneof level {
    // The data is small enough to be stored inline.
    bytes contents_inline = 1;

    // The data is too large to be stored inline.
    FileContents contents_external = 2;
  }
}
```

`Value` then uses it for both kinds:

```proto
message Value {
  oneof kind {
    // ...
    bonanza.model.filesystem.MaybeInlineFileContents str = 31;
    bonanza.model.filesystem.MaybeInlineFileContents bytes = 32;
  }
}
```

The old `string str = 22` and `bytes bytes = 5` fields are removed. An
added benefit: the inline form is `bytes`, not proto3 `string`, which
the Go protobuf library requires to be valid UTF-8. That's why
`repo.go` currently has to sanitize data with `bytesToValidString()`
before it can be stored. With this layout, strings can hold arbitrary
bytes, as in Bazel, and that sanitizing goes away.

`pkg/model/filesystem` gets the matching helpers, so every user of the
message gets the same canonical encoding:

- a function that creates a `MaybeInlineFileContents` from data, given
  the `FileCreationParameters` and the maximum inline size;
- support in `FileReader` for reading a `MaybeInlineFileContents`, both
  in full and at an offset (`ReadAt()`).

The maximum inline size is a parameter of each call, not part of
`FileCreationParameters`, because how much room there is for inline
data depends on the message it is embedded in. For Starlark values it
is `ValueEncodingOptions.ObjectMinimumSizeBytes`: strings of that size
leave room for the rest of the object that contains them, while larger
strings are worth sharing between objects instead of being copied into
each of them. Moving
`command.Log` and the other places mentioned above over to the new
message is a separate change.

**Canonical encoding.** Data is stored inline exactly when its length
is at most the maximum inline size, and as `FileContents` otherwise.
`FileContents` is chunked with MaxCDC, which is content-defined. So
for a given set of encoding options, a string has exactly one
encoding, and equal runs of bytes in different values end up in
shared chunks. Runs of null bytes become holes, as they would in
files. Readers already handle those transparently.

**Why `FileContents`.** Besides a canonical chunker and a reader that
can seek (`FileReader`), it lets strings and files share data without
copying:

- `repository_ctx.read()`, `module_ctx.read()` and the `stdout`/`stderr`
  of `repository_ctx.execute()` can return strings backed by the
  existing `FileContents`. They don't read anything, and they no longer
  need size limits. The only exception is data within the maximum
  inline size, which the canonical form requires to be stored inline.
  It is small, so reading it is cheap.
- `repository_ctx.file(content = ...)` and
  `ctx.actions.write(content = ...)` can refer to the chunks of a
  storage-backed string directly.

In return, `ValueEncodingOptions` gains `FileCreationParameters`
(chunk encoders and MaxCDC parameters), and `ValueDecodingOptions`
gains a `FileReader`. In `pkg/model/analysis` these are the build's
parameters, obtained through the `FileCreationParametersObject` and
`FileReader` keys, so that strings can share data with files. A
`valueEncodingOptionsFactory` obtains the file creation parameters
from the environment up front, and then creates `ValueEncodingOptions`
given just a context and the current filename. The `FileReader` is
only requested once a string is accessed that isn't stored inline, as
decoding happens deep inside Starlark code.

`Funcode` constants hold strings as `bytes` for the same reason. They
are always stored inline, as they are bounded by the size of the
source file.

**In memory.** `String` stays a concrete Go type, so the common case
has no extra cost. Storage-backed large strings are of type
`*eval.LargeString`, whose contents are provided by a
`LargeStringSource`:

```go
type LargeStringSource interface {
	SizeBytes() int64
	ReadAt(t *Thread, p []byte, off int64) error
}
```

Keeping the semantics of strings in a single type in
`pkg/starlark/eval` means that `pkg/model/starlark` only needs to
provide access to the data. Sources may implement
`LargeStringSourceComparer` to report that two strings refer to the
same data without reading it. `pkg/model/starlark` does so by comparing
the references of the roots of their Merkle trees.

These operations work on large strings without reading all of them
into memory:

- `type()`, `len()` and truth;
- encoding, by returning the original message;
- `==`, ordering, `in`, and `startswith()`/`endswith()` without
  `start`/`end`, by streaming. Strings with equal references are equal
  without being read;
- `Hashable.Hash()`, by streaming. `maphash` produces the same result
  whether a string is written in pieces or all at once, so this
  matches the hash of an equal in-memory `String`, and both can be
  used interchangeably as keys of dicts;
- `repr()`, by streaming into the `Printer`;
- indexing and slicing, by reading only the requested range.

All other operations convert to an in-memory `String` first. Examples
are `+`, `%`, `str()`, `print()`, `hash()`, `format()`, `split()` and
`replace()`. The amount of data converted this way is limited per
thread (`Thread.MaximumLargeStringConversionBytes`, which
`pkg/model/analysis` sets to 64 MiB),
and an operation that exceeds the limit fails with an error that says
so. The other direction needs nothing special: a large `String` built
in memory (e.g. by `"\n".join(...)`) stays a Go string until it is
encoded, and is chunked at that point.

**UTF-8.** Starlark strings may contain arbitrary bytes, but Go code
outside the interpreter should only use `string` for UTF-8. So
`eval.AsString()` fails for strings that aren't valid UTF-8, while
`eval.AsBytes()` returns the contents of any string as a `[]byte`.
Both accept either kind of string, converting large strings. The
unpackers use them: `unpack.String` for things like labels and paths,
and `unpack.Bytes` for arbitrary data, such as the contents of files
written by `repository_ctx.file()` and `ctx.actions.write()`, and the
substitutions of templates. Inside `pkg/starlark/eval`, `String` stays
a Go string, holding an immutable sequence of bytes.

Bazel's dialect of Starlark has no `bytes` type, so the interpreter
doesn't have one either. `Value.bytes` uses `MaybeInlineFileContents`
as well, but values of that kind can't be decoded yet.

Code in `pkg/model/analysis` that inspects encoded values directly
(e.g. platform properties and build setting values) uses
`model_starlark.GetString()`, which reads strings that aren't stored
inline using a `FileReader`, and fails for strings that aren't valid
UTF-8.

`repository_ctx.file()` and `ctx.actions.write()` reuse the
`FileContents` of large strings that are backed by storage, using
`model_starlark.NewStoredStringFileContentsUnpackerInto()`. They don't
read these strings, so they aren't subject to the conversion limit.

Not done yet:

- Letting `repository_ctx.read()`, `module_ctx.read()` and
  `repository_ctx.execute()` return strings backed by existing
  `FileContents`. These still read data into memory, subject to their
  existing size limits.
- Rendering the contents of large strings in `pkg/model/render`. They
  are rendered as links to the objects that hold their contents.

## Functions

### Code objects in storage

`pkg/starlark/compile` emits one `compile.Funcode` per `def`/`lambda`,
plus one for the top level of the file. A `Funcode` is self-contained.
Its constants, names, loaded symbols and nested functions are stored in
tables of its own, rather than in tables shared by the whole file. Each
one can therefore be stored as a `Funcode` message of its own.

The code of all functions and lambdas declared at the top level of a
file is stored in a B-tree in `CompiledProgram.functions`, sorted by
name. Nested functions are stored inside the message of the function
containing them, so a function and everything nested in it are always
read together. The name of a function isn't part of its `Funcode`. It's
stored next to it, in the B-tree entry or `Funcode.NestedFunction`, and
is left empty for lambdas.

The line at which a function is declared isn't part of its `Funcode`
either, so that adding or removing lines elsewhere in a file doesn't
affect the code of its functions. The lines of top-level functions are
stored in `CompiledProgram.top_level_function_lines`, in the same order
as the B-tree. Parent entries of the B-tree hold the number of functions
contained in them, so that the index of a function is determined while
searching for it. Nested functions store their line relative to that of
the enclosing function.

The compiler itself only produces Go structs. Converting them to
messages is the job of `pkg/model/starlark`, so that the compiler
stays independent of storage.

`model_starlark_pb.Function` refers to code by name instead of by
reference:

```proto
message Function {
  // Module whose globals the code resolves against, and whose
  // CompiledProgram contains the code.
  string filename = 1;

  message Name {
    // Empty for lambdas.
    string name = 1;
    // Zero for the first occurrence of the name within the enclosing
    // function.
    int32 disambiguation_index = 2;
  }

  // Path of the function within the file, one element per level of
  // nesting.
  repeated Name name_path = 2;

  // Default values of parameters and free variables.
  Closure closure = 3;
}
```

This means that values that embed a function, such as structs,
providers and rule definitions, only change if the function's name or
closure does, not if its body does. Whoever calls the function looks up
its code through `CompiledProgramResolver`, and thereby gets a
dependency on it. A closure's values were produced by running its
parent, so anything holding one already depends on the parent's code.

The module is referenced by name rather than by object reference
because a module's globals contain its own functions. Referencing the
module by content would create a cycle. As every function stores its
own default values, functions declared at the top level of a file no
longer need to be recreated by executing the file again.

### Calling a function

The core defines a function as an interface (`eval.Function`), so it
doesn't need to know about storage. Its `Funcode()`, `Module()`,
`Defaults()` and `FreeVars()` methods take a `*Thread`, so that an
implementation backed by storage can load them when the function is
first called.

`DecodeFunction()` returns such an implementation. Its module resolves
names as follows:

- **Globals of the module** and **`load()` bindings** go through the
  `GlobalResolver` stored in the thread, which in the context of an
  evaluation requests `CompiledBzlFileGlobal`. Values are cached on the
  thread for the duration of the evaluation, never across evaluations.
- **Predeclared names** come from `ValueDecodingOptions.BzlFileBuiltins`.

`load()` bindings are recorded in `Funcode.imports` as `(canonical
label, symbol)` pairs. `CanonicalizeImports()` rewrites the module
strings of a compiled program to canonical labels before it is
executed, so that the code of its functions can be written to storage.

Decoded `Funcode`s are cached on the thread to avoid repeated
decoding. The VM's recursion check doesn't
depend on this: it identifies functions by their filename and the
position of their declaration. Comparing `Funcode`s wouldn't work, as
identical functions in different files may share the same object.

So calling a function declared in another file adds dependency edges
only on the globals it actually touches. It never executes the file's
top-level code again, and `CompiledBzlFileFunctionFactory` goes away.

Private globals (`_foo`) also have to be encoded in `CompiledProgram`
now, because functions in the same file refer to them.
`EncodeCompiledProgram()` scans the bytecode of the program's functions
for `GLOBAL` instructions, and only encodes the private globals that
are referenced this way.

Rules, providers, macros, etc. refer to functions using plain
`eval.Function` values.

### Lazy `load()`

`load("//x:defs.bzl", "a", b = "c")` produces `Import` bindings in the
resolver, not code that runs eagerly. During top-level execution of the
loading file:

- The caller resolves the module string of each `LoadedSymbol` to a
  canonical label, using the repo mapping that `preloadBzlGlobals()`
  uses today.
- `CompiledBzlFile` checks that each loaded symbol exists by looking at
  the sorted `Struct.Fields.keys` of the loaded file's globals. This
  keeps Bazel's "symbol not found" error at load time without decoding
  any values.
- A value is fetched the first time the binding is read.

## Thread and VM

```go
type Thread struct {
	ctx      context.Context
	frames   []frame       // explicit call stack
	steps    uint64
	maxSteps uint64
	print    func(t *Thread, msg string) error
	locals   map[any]any   // typed keys instead of string constants
}
```

- **Explicit frame stack.** Starlark-to-Starlark calls push a frame on
  `Thread.frames` instead of recursing in Go. Builtins that call back
  into Starlark (`sorted(key=...)`, computed provider fields,
  `Args.add_all(map_each=...)`) still recurse in Go, which is fine.
  The explicit stack gives cheap backtraces and makes recursion
  detection trivial. As in Bazel, recursion is never permitted.
- **Cancellation.** Check `ctx.Err()` every N steps, alongside the step
  limit.
- **Errors.** All wrapping uses `%w` (this replaces `unwrap.diff`).
  `ErrMissingDependency` passes through unchanged and is never turned
  into an `EvalError`. Callers keep using
  `errors.Is(err, evaluation.ErrMissingDependency)`.
- **Bytecode.** A stack-based instruction set similar to
  `go.starlark.net`'s: proven, simple, and easy to compile to. The
  differences are:
  - `GLOBAL`/`SETGLOBAL` refer to globals by name rather than by
    index, so that a function can be called without the rest of its
    file.
  - `IMPORT` reads a symbol bound by `load()`, loading it on first
    use, and replaces `LOAD`. A load statement compiles to nothing.
  - Call instructions encode their positional and named argument
    counts in 16 bits each, instead of 8. The bits of both counts are
    interleaved, so that calls with few arguments have small operands.
  - Type annotations compile to nothing, and `cast(T, x)` compiles to
    `x`.

  The format is versioned (`compile.Version`), and a version mismatch
  is rejected rather than misinterpreted.
- **Determinism.** Anything Starlark code can observe may end up in
  storage, so all of it must be deterministic across processes and
  releases:
  - the output of `hash()` (see [Equality and hashing](#equality-and-hashing));
  - the [key ordering](#key-ordering) of stored dicts and sets;
  - the iteration order of all core types;
  - the order of `dir()`;
  - float and `%` formatting;
  - `repr()` of functions and builtins, which never includes addresses;
  - the order in which builtins produce values from Go maps.

  The reference-equal identifier generator (depsets) becomes a typed
  thread local. Only internal hash tables may use per-process
  randomness.

### Builtins

```go
type Args struct {
	Positional []Value
	Named      []NamedArg
}

type Builtin struct {
	name     string
	fn       func(t *Thread, b *Builtin, args Args) (Value, error)
	receiver Value // for methods such as list.append
}
```

Arguments are unpacked with `eval.UnpackArgs()`, which is modelled
after `starlark.UnpackArgs()`. It accepts pointers to variables of the
core types, or implementations of `eval.Unpacker`. The unpackers in
`pkg/starlark/unpack` are ported to the new interpreter, and
`unpack.Bind()` turns an `UnpackerInto[T]` into an `eval.Unpacker`.
Their methods already took a `*Thread`, so they port by changing
types. Unpackers of composite values, such as `unpack.List`, accept
any implementation of `eval.List`/`eval.Tuple`, so they work for
storage-backed values as well. So far only the unpackers needed by
`MODULE.bazel` files have been ported.

The standard library lives in `pkg/starlark/eval` itself, rather than
in a separate package, because the VM looks up the methods of the core
types (`x.append`) when executing `ATTR`. It implements:
- the builtins of Bazel's Starlark;
- the methods of `str`, `list` and `dict`;
- Bazel 8's `set`;
- `range`;
- the `json` module.

This includes the behaviour that `go.starlark.net` previously needed
patches for, such as keyword arguments for `dict.get()`/`dict.pop()`.
Decisions follow Bazel where it differs from go.starlark.net:

- **Case and classification are ASCII only.** Strings are byte
  strings, so `upper()`, `lower()`, `isalpha()` etc. only consider
  ASCII characters. Bazel treats each byte as a Latin-1 character,
  which corrupts UTF-8 sequences when changing case. We leave them
  unchanged instead, which gives the same result as Bazel for most
  real strings.
- **Mutation checks come before anything else.** Mutating a frozen
  set or dict fails even if the operation would have been a no-op
  (e.g. `discard()` of a missing element).
- **`hash()` hashes bytes,** as described under
  [Equality and hashing](#equality-and-hashing).
- **`repr()` writes non-printable bytes as octal escapes** (e.g.
  `"\377"`), as Bazel's scanner doesn't support `\x` escapes.
- **Spelling suggestions use Bazel's threshold:** one edit for every
  two characters.
- **Some parts of go.starlark.net don't exist in Bazel, so they are
  left out:** `chr()`, `ord()`, the code point iteration methods,
  `%c`, and `b"..."` literals.

Conformance is tested using go.starlark.net's `testdata/*.star` files
(BSD-3-Clause), adapted to Bazel: `assert` is a reserved word in
Bazel, error messages follow Bazel, and the Unicode-specific tests are
removed.

## Impact on `pkg/model/evaluation` and `pkg/model/analysis`

| Today | After |
|---|---|
| `CompiledBzlFileDecodedGlobals` → `starlark.StringDict` (native) | Removed. Consumers decode the globals they need from `CompiledBzlFile`, when they are first used. |
| `CompiledBzlFileFunctionFactory` → `*starlark.FunctionFactory` (native) | Removed. Functions are called from their `Funcode` + module. |
| `getBzlFileBuiltins()` decodes all of `exports.bzl`'s `exported_toplevels`/`exported_rules` into a `StringDict` on every evaluation | Still decoded on the thread of every evaluation that needs them, from the `CompiledBzlFile` messages of the builtins modules. Decoding is lazy, so only the builtins that are used are read. Stored functions obtain them through `ValueDecodingOptions.PredeclaredResolver`. |
| `CompiledProgram.code` stored only if `needsCode` | Replaced by a B-tree of `Funcode`s in `CompiledProgram`, which functions refer to by name. |
| `NameAndExtractGlobals()` names rules, providers, etc. after the `.bzl` file has finished executing, picking the alphabetically first global if a value is bound to several | `BzlModule.SetGlobal()` names them as soon as they are assigned to a global, so the name is visible to the rest of the file and the first assignment wins, as in Bazel. Values that are never bound to a global directly (e.g., stored in a dict) stay unnamed. |

That removes every Starlark-related native value key. The native keys
that remain (`FileReader`, `ActionReaders`, `*CreationParametersObject`,
encoder objects, etc.) are derived from message values and could
become a per-process cache keyed by those messages. That's a separate
change, but this design removes the last thing that would block it.

## Migration plan

Values can't cross between two interpreters, and all Bazel types in
`pkg/model/starlark` would have to exist for both. So running both
interpreters side by side in one binary isn't practical. Instead:

1. **Write `pkg/starlark/syntax` and `pkg/starlark/resolve`**, with
   ported upstream scanner, parser and resolver tests, plus tests for
   the Bazel dialects and type annotations. Validate the parser by
   parsing every `.bzl` and `BUILD` file in the Bazel Central Registry
   and in Bazel's own repository with both parsers, and comparing the
   resulting trees. This can land on `main` right away, as unused code.
2. **Build the core** (`pkg/starlark/compile` and `pkg/starlark/eval`)
   without any storage. Port `go.starlark.net`'s `testdata/*.star`
   conformance tests (BSD-3-Clause) and add Bazel-dialect cases. This can also land
   on `main` early, as unused code.
3. **Port `MODULE.bazel` evaluation.** The `MODULE.bazel` parser
   (now `pkg/moduledotbazel`) only depends on the core types and a few
   unpackers, so it can move over first. Its handler interfaces take
   `eval.Value`s and the calling `*eval.Thread`, and consumers in
   `pkg/model/analysis` encode attributes with
   `model_starlark.EncodeStructFields()`. This was validated by
   evaluating every `MODULE.bazel` file in a Bazel output base with
   both implementations and comparing the calls made to the handler.
4. **Port the rest of the unpackers and `pkg/model/starlark`**
   on a branch.
   `thread.diff` already threads `*Thread` through every method, so
   most type implementations port by changing signatures. The work
   that's actually new is `storedList`/`storedDict`/`storedTuple`/
   `storedSet`, the large string and bytes types, `Function` +
   `Module`, and replacing `DecodeValue()`'s eager decoding with lazy
   wrappers. The memoization in `protoNamedFunctionDefinition`,
   `reloadingRuleDefinition` and `reloadingMacroDefinition` goes away,
   following
   [Memoization and dependency tracking](#memoization-and-dependency-tracking).
   This is done in place, so `pkg/model/starlark` doesn't build until
   the port is complete. It's split in two batches:

   - **Plain value types** (done): `Label`, `struct` and provider
     instances, `depset`, `select()`, `File`, `Target`, `path`,
     `config_common.toolchain_type`, `exec_group`, macro visibility,
     and the remaining unpackers. Decisions made along the way:
     - `EncodeValue()` takes the thread, and `path` is a slice of the
       mutable composite values that are being encoded. The
       `needsCode` result is gone (see [Functions](#functions)).
     - `ValueDecodingOptions` remains stored in a thread local
       variable. All values that are used within a single thread are
       assumed to be decoded with the same options, so values that
       decode lazily obtain them from the thread when they are
       accessed, instead of keeping a pointer to them. This means
       that code in `pkg/model/analysis` that decodes values with
       different `LabelCreator`s within a single thread (e.g.,
       `configured_target.go`) needs to be restructured.
     - `LabelCreator` functions are now called lazily. Ones that
       record missing dependencies in a variable that is checked
       right after decoding need to be revisited as well.
     - `CurrentFilePackage()` uses the innermost frame on the call
       stack that belongs to a Starlark file, and returns an error
       instead of panicking.
     - `repr()` of labels and structs follows Bazel: `Label("@@a//b:c")`
       and `struct(a = 1)`, or the provider's name for provider
       instances.
   - **Types holding functions** (done): `provider()`, rules, macros, aspects, subrules, transitions,
     repository rules, module extensions, tag classes, attributes,
     build settings, `native.existing_rules()` and the builtins of
     `.bzl` and `BUILD` files, together with the per-function code
     storage described in [Functions](#functions). `REPO.bazel` files
     are evaluated by the new interpreter as well. Error messages of
     argument unpacking now follow `eval.UnpackArgs()`, e.g.
     `macro(): for parameter inherit_attrs: ...`.
5. **Port `pkg/model/analysis`** (done) on the same branch. Decisions
   made along the way:
   - The native keys `CompiledBzlFileDecodedGlobals` and
     `CompiledBzlFileFunctionFactory` are removed. Values that are
     decoded lazily cache state without synchronization, so they may
     not be shared between evaluations. The builtins exported by the
     `exports.bzl` files of builtins modules are decoded on the thread
     of every evaluation that needs them, from their `CompiledBzlFile`
     messages. Stored functions obtain them through
     `ValueDecodingOptions.PredeclaredResolver`, which computes them on
     first use.
   - For the same reason, `RuleImplementationWrappers` became a
     message-valued key, and `RepositoryRuleObject` holds the
     definition message of the repository rule, which is decoded by
     each evaluation that calls it.
   - `.bzl` and `BUILD` files are compiled by `loadProgram()`. It
     canonicalizes the labels of `load()` statements, and checks that
     all loaded symbols exist before any code is executed. Loaded
     symbols are decoded when first used.
   - Globals of other files, both for `load()` and for the
     `GlobalResolver`, are obtained through `CompiledBzlFile`, using
     the same lists of builtins modules as before. This is coarser
     than requesting `CompiledBzlFileGlobal`, whose key doesn't
     contain the list of builtins modules. Adding it would make
     dependencies more precise.
   - Attribute values of configured targets are decoded with
     `DecodeAttrValuePart()`, as their labels are converted to `Target`
     objects of a specific configuration.
   - go.starlark.net and its patches are removed, and the ported
     unpackers take the place of the old ones in
     `pkg/starlark/unpack`. Also removed are the tests that compared
     the parser and resolver against upstream, as they depended on
     go.starlark.net.
6. **Validate** by building a corpus of real workspaces with `main` and
   the branch and comparing the resulting action digests. Values like
   `CompiledBzlFile` change by design, but the actions sent to workers
   should be bit-for-bit identical. Because Bonanza's encoding is
   deterministic, any difference points straight at a behaviour change.

## Risks

- **Latency of lazy reads.** Data that used to be read in bulk is now
  read node by node, in the middle of evaluation. Mitigations: the
  parsed object pool, and measuring on large `deps` graphs. If that
  isn't enough, prefetching (see [Future work](#future-work)).
- **Interface dispatch** on hot list/dict operations. Mitigated by the
  fast paths for in-memory implementations.
- **Dialect drift.** Users notice small differences in error messages,
  `%` formatting or float printing. Conformance tests plus action
  digest comparison should catch most of these.
- **Stricter dialects break existing code.** Enforcing Bazel's
  `BUILD`/`.bzl` restrictions that Bonanza doesn't enforce today may
  reject files that currently load. That's correct behaviour, but it
  can be rolled out as warnings first.
- **Keeping up with Bazel's syntax.** We now track the Starlark spec
  ourselves. The annotation syntax in particular is still changing
  (bazelbuild/starlark#362 is still open). Gating it behind a `Dialect`
  option keeps changes contained.
- **Parser differences.** A new parser may accept or reject files
  differently from go.starlark.net. Comparing parse trees on the Bazel
  Central Registry corpus should catch this before the switch.

## Future work

- **Canonical concatenation.** `a + b` on large immutable lists,
  strings or bytes could reuse the existing subtrees of both operands
  without reading them, instead of converting to memory and
  re-encoding. Doing this naively makes the encoding of a value depend
  on how it was built, which lowers cache hit rates for keys containing
  it. So it first needs concatenation operations in
  `pkg/model/core/btree` that produce the same tree as encoding the
  result from scratch. For `FileContents`, that means re-chunking only
  around the point where the operands meet. Until then, concatenation
  converts to memory.
- **Prefetching.** Reading nodes ahead of time when iterating over
  stored lists, and fetching the leaves that upcoming insertion-order
  positions point to when iterating over large stored dicts and sets.
  Worth investigating once profiles show read latency matters.
- **Park goroutines instead of restarting.** Today an
  `ErrMissingDependency` deep inside a rule implementation throws away
  all work, and the whole evaluation reruns later. Instead,
  `Environment.Get*Value()` could block the compute function's
  goroutine until the dependency has been computed. This costs some
  memory for parked goroutines, which is likely acceptable. It isn't
  specific to Starlark and needs nothing from the VM, so the explicit
  frame stack is not a prerequisite. Points to work out in
  `RecursiveComputer`:
  - **Dependency cycles.** Parked goroutines must still be detected and
    reported as cycles, rather than waiting on each other forever.
  - **Parallelism.** Today, code such as `constraintValuesToConstraints()`
    collects all missing dependencies in one pass, so they are computed
    in parallel. Blocking on each `Get` in turn would discover them one
    at a time. A way to request several keys first and then block once
    would keep that parallelism.
  - **Memory.** Bounding the number of parked goroutines, or the memory
    they hold, with restarting as the fallback.
- Lint-style checks in the resolver (unused loads, shadowed builtins)
  at `CompiledBzlFile` time.
