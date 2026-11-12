package utf8stat

import (
	"bytes"
	"math/rand"
	"testing"
	"unicode/utf8"
)

// Byte sequences biased towards UTF-8 edge cases.
var pieces = [][]byte{
	[]byte("a"), []byte("\n"), []byte("é"), []byte("€"), []byte("𝄞"),
	{0xED, 0x9F, 0xBF},
	{0xED, 0xA0, 0x80}, // last BMP before surrogates, surrogate
	{0xE0, 0xA0, 0x80},
	{0xE0, 0x9F, 0xBF}, // smallest 3-byte, overlong
	{0xF0, 0x90, 0x80, 0x80},
	{0xF0, 0x8F, 0xBF, 0xBF}, // smallest 4-byte, overlong
	{0xF4, 0x8F, 0xBF, 0xBF},
	{0xF4, 0x90, 0x80, 0x80}, // U+10FFFF, too large
	{0x00},
	{0x80},
	{0xBF},
	{0xC0},
	{0xC2},
	{0xE1},
	{0xF5},
	{0xFF},
}

func randomData(r *rand.Rand) []byte {
	var b []byte
	n := 1 + r.Intn(12)
	for i := 0; i < n; i++ {
		if r.Intn(4) == 0 {
			b = append(b, byte(r.Intn(256)))
		} else {
			b = append(b, pieces[r.Intn(len(pieces))]...)
		}
	}
	return b
}

// fold combines stats for data[lo:hi] using a random tree shape.
func fold(r *rand.Rand, chunks [][]byte) Stat {
	if len(chunks) == 1 {
		return Compute(chunks[0])
	}
	m := 1 + r.Intn(len(chunks)-1)
	return Combine(fold(r, chunks[:m]), fold(r, chunks[m:]))
}

func TestEncoding(t *testing.T) {
	for _, c := range []struct {
		data string
		want Stat
	}{
		{"hello", 1},
		{"a\nb", 1<<2 | 1},
		{"a\nb\n", 1<<2 | 1<<1 | 1},
		{"\n", 1<<1 | 1},
		{"\xa9\n", 0<<11 | 1<<8 | 2<<4 | 1<<2 | 0b10}, // Tail of "é", then newline.
		{"\xff", Invalid},
		{"a\x00b", Invalid},
		{"é\n\x80", Invalid}, // Mixed content.
		{"\x80", 0b0100},     // Lone continuation byte, class 80.
	} {
		if got := Compute([]byte(c.data)); got != c.want {
			t.Errorf("Compute(%q) = %#b, want %#b", c.data, got, c.want)
		}
	}
}

func TestMatchesWholeFile(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for iter := 0; iter < 2_000_000; iter++ {
		data := randomData(r)

		// Split into random non-empty chunks.
		var chunks [][]byte
		for rest := data; len(rest) > 0; {
			n := 1 + r.Intn(len(rest))
			chunks = append(chunks, rest[:n])
			rest = rest[n:]
		}

		whole := Compute(data)
		got := fold(r, chunks)

		if got != whole {
			t.Fatalf("%x split as %x: combined %#x, whole %#x", data, chunks, got, whole)
		}
		if want := isText(data); got.IsValidFile() != want {
			t.Fatalf("%x: IsValidFile = %v, want %v", data, got.IsValidFile(), want)
		}
		if iter%16 == 0 {
			if want := partiallyValid(data); got.IsValidPartialFile() != want {
				t.Fatalf("%x: IsValidPartialFile = %v, want %v", data, got.IsValidPartialFile(), want)
			}
		}
		if got != Invalid && got.HasTrailingNewline() != (data[len(data)-1] == '\n') {
			t.Fatalf("%x: HasTrailingNewline = %v", data, got.HasTrailingNewline())
		}
		if n := got.LineBreaks() + b2u(got.HasTrailingNewline()); got.IsValidFile() && n != uint64(bytes.Count(data, []byte("\n"))) {
			t.Fatalf("%x: wrong newline count %d", data, n)
		}
	}
}

func TestNewlineSaturation(t *testing.T) {
	clean := func(n uint64, tnl bool) Stat {
		return state{kind: kindGeneral, lineBreaks: n, trailingNewline: tnl}.encode()
	}
	// Ends with the lead byte of a 2-byte rune.
	open := func(n uint64) Stat {
		return state{kind: kindGeneral, missing: 1, mask: maskAny, lineBreaks: n}.encode()
	}
	// Starts with a continuation byte of class A0.
	closing := func(n uint64) Stat {
		return state{kind: kindGeneral, leading: 1, class: classA0, lineBreaks: n}.encode()
	}

	if got := Combine(clean(MaxLineBreaks-1, true), clean(5, false)); got != clean(MaxLineBreaks, false) {
		t.Errorf("clean+clean: got %d line breaks", got.LineBreaks())
	}
	if got := Combine(open(MaxLineBreaks), closing(MaxLineBreaks)); got != clean(MaxLineBreaks, false) {
		t.Errorf("open+closing: got %d line breaks", got.LineBreaks())
	}

	// Associativity near the limit.
	vals := []uint64{0, 1, 2, MaxLineBreaks / 2, MaxLineBreaks - 1, MaxLineBreaks}
	for _, x := range vals {
		for _, y := range vals {
			for _, z := range vals {
				for m := 0; m < 8; m++ {
					a, b, c := clean(x, m&1 != 0), clean(y, m&2 != 0), clean(z, m&4 != 0)
					if Combine(Combine(a, b), c) != Combine(a, Combine(b, c)) {
						t.Fatalf("not associative for %d, %d, %d, %03b", x, y, z, m)
					}
				}
				// A rune split between the first and second chunk.
				a, b, c := open(x), closing(y), clean(z, true)
				r := Combine(Combine(a, b), c)
				if r == Invalid || r != Combine(a, Combine(b, c)) {
					t.Fatalf("not associative (split) for %d, %d, %d", x, y, z)
				}
			}
		}
	}
}

// partiallyValid reports whether some bytes can be prepended and appended to
// data to make it valid UTF-8. The prefixes cover every state of the UTF-8
// validator, and the suffixes cover every class of continuation byte.
func partiallyValid(data []byte) bool {
	if bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	prefixes := [][]byte{{}, {0xC2}, {0xE1}, {0xE0}, {0xED}, {0xF1}, {0xF0}, {0xF4}}
	suffixes := [][]byte{{}}
	for i := 0; i < 3; i++ {
		for _, s := range suffixes {
			if len(s) == i {
				for _, c := range []byte{0x80, 0x90, 0xA0} {
					suffixes = append(suffixes, append(append([]byte{}, s...), c))
				}
			}
		}
	}
	for _, p := range prefixes {
		for _, s := range suffixes {
			if utf8.Valid(append(append(append([]byte{}, p...), data...), s...)) {
				return true
			}
		}
	}
	return false
}

// isText reports whether data is valid UTF-8 without any NUL bytes.
func isText(data []byte) bool {
	return utf8.Valid(data) && bytes.IndexByte(data, 0) < 0
}
