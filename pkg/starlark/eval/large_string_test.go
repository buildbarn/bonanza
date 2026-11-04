package eval_test

import (
	"context"
	"strings"
	"testing"

	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"

	"github.com/stretchr/testify/require"
)

// stringSource is an implementation of LargeStringSource that is
// backed by an in-memory string. It counts the number of bytes that
// are read, so that tests can assert that operations don't read more
// than necessary.
type stringSource struct {
	data      string
	readBytes int64
}

func (s *stringSource) SizeBytes() int64 {
	return int64(len(s.data))
}

func (s *stringSource) ReadAt(t *eval.Thread, p []byte, off int64) error {
	copy(p, s.data[off:])
	s.readBytes += int64(len(p))
	return nil
}

func TestLargeString(t *testing.T) {
	// Construct a string that is larger than the size of the reads
	// that are performed when streaming. Place a multi-byte UTF-8
	// sequence on the boundary between reads, and include a byte
	// that is not valid UTF-8.
	data := strings.Repeat("a", 1<<16-1) + "é\x80" + strings.Repeat("xyz", 100000) + "needle" + strings.Repeat("b", 1000)

	exec := func(t *testing.T, src string, configure func(*eval.Thread)) (*stringSource, error) {
		source := &stringSource{data: data}
		predeclared := map[string]eval.Value{
			"large":      eval.NewLargeString(source),
			"large_copy": eval.NewLargeString(&stringSource{data: data}),
			"small_copy": eval.String(data),
		}
		f, err := syntax.Parse("test.star", []byte(src), syntax.Options{})
		require.NoError(t, err)
		universe := eval.Universe()
		m, err := resolve.File(
			f,
			&resolve.BzlDialect,
			func(name string) bool { _, ok := predeclared[name]; return ok },
			func(name string) bool { _, ok := universe[name]; return ok },
		)
		require.NoError(t, err)
		thread := eval.NewThread(context.Background(), universe)
		if configure != nil {
			configure(thread)
		}
		return source, eval.ExecFile(thread, compile.File(f, m), eval.NewInMemoryModule("test.star", predeclared, nil))
	}

	t.Run("Streaming", func(t *testing.T) {
		// None of these operations should load the string into
		// memory, meaning they should succeed even if the
		// conversion limit is set to a very small value.
		_, err := exec(t, `
def check(cond, msg):
    if not cond:
        fail(msg)

check(type(large) == "string", "type")
check(large, "truth")
check(len(large) == len(small_copy), "len")
check(large == small_copy and small_copy == large, "== String")
check(large == large_copy, "== LargeString")
check(large != small_copy + "x" and small_copy + "x" != large, "!= String")
check(large < small_copy + "x" and small_copy[:-1] < large, "< String")
check(not (large < large_copy), "< LargeString")
check(hash(small_copy) == hash(small_copy), "hash")
check({small_copy: 1}[large] == 1 and {large: 1}[small_copy] == 1, "dict key")
check(large in [small_copy] and small_copy in [large], "list membership")
check(large[0] == "a" and large[-1] == "b", "index")
check(large[65534:65538] == "aé\200", "slice")
check(large[65537:65533:-1] == "\200\251\303a", "negative step")
check(large[:10:3] == "aaaa", "step")
check("needle" in large and "é\200xyz" in large and "missing" not in large, "in")
check(large.startswith("aaa") and large.startswith(("x", "aa")) and not large.startswith("b"), "startswith")
check(large.endswith("bbb") and not large.endswith("a"), "endswith")
`, func(thread *eval.Thread) {
			thread.MaximumLargeStringConversionBytes = 1
		})
		require.NoError(t, err)
	})

	t.Run("Repr", func(t *testing.T) {
		// repr() should yield the same result as for an
		// in-memory string, even if multi-byte sequences cross
		// the boundaries of reads.
		thread := eval.NewThread(context.Background(), eval.Universe())
		got, err := eval.Repr(thread, eval.NewLargeString(&stringSource{data: data}))
		require.NoError(t, err)
		require.Equal(t, syntax.Quote(data), got)
	})

	t.Run("Hash", func(t *testing.T) {
		// Large strings should hash identically to in-memory
		// strings, so that they can be used interchangeably as
		// keys of dicts.
		thread := eval.NewThread(context.Background(), eval.Universe())
		got, err := eval.Hash(thread, eval.NewLargeString(&stringSource{data: data}))
		require.NoError(t, err)
		want, err := eval.Hash(thread, eval.String(data))
		require.NoError(t, err)
		require.Equal(t, want, got)
	})

	t.Run("PartialReads", func(t *testing.T) {
		// Indexing, slicing and comparing prefixes should only
		// read the parts of the string that are needed.
		source, err := exec(t, `
x = large[1000]
y = large[:10]
z = large.startswith("aaaa")
`, nil)
		require.NoError(t, err)
		require.Equal(t, int64(15), source.readBytes)
	})

	t.Run("Conversion", func(t *testing.T) {
		// Other operations convert the string to an in-memory
		// string.
		_, err := exec(t, `
def check():
    if large.upper() != small_copy.upper():
        fail("upper")
    if large + "x" != small_copy + "x":
        fail("concatenation")

check()
`, func(thread *eval.Thread) {
			thread.MaximumLargeStringConversionBytes = 1 << 30
		})
		require.NoError(t, err)
	})

	t.Run("ConversionWithoutLimit", func(t *testing.T) {
		// Conversions are not permitted unless a limit is set.
		_, err := exec(t, `
large.upper()
`, nil)
		require.ErrorContains(t, err, "would exceed the limit of 0 bytes")
	})

	t.Run("ConversionLimit", func(t *testing.T) {
		// The amount of data that is converted is limited per
		// thread.
		_, err := exec(t, `
large.upper()
large.lower()
`, func(thread *eval.Thread) {
			thread.MaximumLargeStringConversionBytes = int64(len(data)) + 1
		})
		require.ErrorContains(t, err, "would exceed the limit of")
	})
}
