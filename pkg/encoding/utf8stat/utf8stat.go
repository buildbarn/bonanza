// Package utf8stat computes a compact, recursively combinable summary of a
// non-empty chunk of bytes: whether it can be part of a valid UTF-8 stream
// that contains no NUL bytes, how many line breaks it contains, and whether
// it ends with a newline.
//
// Line breaks are newlines that are followed by more data, i.e., all
// newlines except a trailing one. For a complete file, the number of lines
// is the number of line breaks plus one, regardless of whether the last line
// is terminated by a newline. The trailing newline is stored as a separate
// flag. This makes every bit pattern meaningful (a set flag doesn't require
// a nonzero count), and saves a bit for files ending with a newline: "foo\n"
// encodes as 0b11, not 0b111.
//
// Stats form a semigroup: Combine(a, b) describes a's chunk immediately
// followed by b's chunk, and is associative. Chunks are never empty, so
// there is no identity element.
package utf8stat

// Stat is the encoded summary of a chunk. The encoding is chosen so that
// common values are small, making them cheap to store as a varint:
//
//	0                    invalid (binary, or mixed content)
//	lb<<2 | tnl<<1 | 1   valid UTF-8, starting and ending on rune boundaries
//	                     (the common case)
//	lb<<11 | end<<6 | class<<4 | leading<<2 | 0b10
//	                     valid UTF-8, but starting and/or ending in the
//	                     middle of a rune
//	class<<4 | leading<<2 | 0b00, with leading in 1-3
//	                     chunk consists only of 1-3 continuation bytes
//
// Fields:
//
//	lb       number of line breaks, i.e., newlines excluding a trailing one.
//	         Clamped to MaxLineBreaks (53 one-bits) in all encodings, so that
//	         converting between them never loses information.
//	leading  continuation bytes at the start, completing a rune begun in a
//	         preceding chunk (0-3)
//	class    class of the first of those bytes (only set if leading > 0)
//	missing  continuation bytes a following chunk must provide (0-3)
//	mask     classes allowed for the next continuation byte (only set if
//	         missing > 0)
//	tnl      whether the chunk ends with a newline (only set if missing == 0)
//	end      the state at the end of the chunk (5 bits): tnl<<2 if
//	         missing == 0, or mask<<2 | missing otherwise. A chunk that ends
//	         mid-rune can't end with a newline, so these never overlap.
type Stat uint64

const Invalid Stat = 0

// Classes of continuation bytes. Only the byte directly following a lead
// byte has a restricted range, and all restrictions can be expressed as a
// set of these classes.
const (
	class80 = 0 // 0x80-0x8F
	class90 = 1 // 0x90-0x9F
	classA0 = 2 // 0xA0-0xBF

	maskAny = 1<<class80 | 1<<class90 | 1<<classA0
)

// MaxLineBreaks is the largest number of line breaks that can be
// represented in all encodings. Larger counts are clamped to it.
const MaxLineBreaks = 1<<(64-11) - 1

// addLineBreaks adds two line break counts, saturating at
// MaxLineBreaks. Saturating addition is associative, so Combine remains
// associative.
func addLineBreaks(a, b uint64) uint64 {
	if a > MaxLineBreaks-b {
		return MaxLineBreaks
	}
	return a + b
}

const (
	kindInvalid = iota
	kindCont    // Chunk consists solely of 1-3 continuation bytes.
	kindGeneral // Chunk contains at least one byte that starts a rune.
)

// state is the decoded form of a Stat.
type state struct {
	kind       int
	leading    int
	class      int
	missing    int
	mask       int
	lineBreaks uint64 // Newlines, excluding the trailing newline.

	// Whether the chunk ends with a newline. Implies missing == 0.
	trailingNewline bool
}

func b2u(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

func (s state) encode() Stat {
	switch s.kind {
	case kindCont:
		return Stat(s.class<<4 | s.leading<<2)
	case kindGeneral:
		if s.leading == 0 && s.missing == 0 {
			return Stat(s.lineBreaks<<2 | b2u(s.trailingNewline)<<1 | 1)
		}
		class, end := 0, b2u(s.trailingNewline)<<2
		if s.leading > 0 {
			class = s.class
		}
		if s.missing > 0 {
			end = uint64(s.mask)<<2 | uint64(s.missing)
		}
		return Stat(s.lineBreaks<<11 | end<<6 | uint64(class)<<4 |
			uint64(s.leading)<<2 | 0b10)
	default:
		return Invalid
	}
}

func (v Stat) decode() state {
	switch {
	case v == Invalid:
		return state{kind: kindInvalid}
	case v&1 != 0:
		return state{
			kind:            kindGeneral,
			mask:            maskAny,
			lineBreaks:      uint64(v) >> 2,
			trailingNewline: v&2 != 0,
		}
	case v&2 != 0:
		s := state{
			kind:       kindGeneral,
			leading:    int(v>>2) & 3,
			class:      int(v>>4) & 3,
			missing:    int(v>>6) & 3,
			mask:       maskAny,
			lineBreaks: uint64(v) >> 11,
		}
		if s.missing > 0 {
			s.mask = int(v>>8) & 7
		} else {
			s.trailingNewline = v&(1<<8) != 0
		}
		return s
	default:
		return state{kind: kindCont, leading: int(v>>2) & 3, class: int(v>>4) & 3}
	}
}

// IsValidFile reports whether the stat describes a complete, valid UTF-8
// file: no dangling continuation bytes at the start and no incomplete rune
// at the end.
func (v Stat) IsValidFile() bool { return v&1 != 0 }

// IsValidPartialFile reports whether the stat describes valid UTF-8, except
// that it may start and/or end in the middle of a rune (e.g., a truncated
// file, or a range of bytes taken from a larger file). This holds for all
// stats other than Invalid, including chunks consisting solely of
// continuation bytes.
func (v Stat) IsValidPartialFile() bool { return v != Invalid }

// LineBreaks returns the number of line breaks in a chunk that is not
// invalid: newlines, excluding the trailing newline (if any). For a complete
// file, the number of lines is LineBreaks() + 1. The total number of
// newlines, as reported by "wc -l", is LineBreaks() plus one if
// HasTrailingNewline().
func (v Stat) LineBreaks() uint64 { return v.decode().lineBreaks }

// HasTrailingNewline reports whether the last byte of a chunk that is not
// invalid is a newline.
func (v Stat) HasTrailingNewline() bool { return v.decode().trailingNewline }

func isContinuation(b byte) bool { return b&0xC0 == 0x80 }

func classOf(b byte) int {
	switch {
	case b < 0x90:
		return class80
	case b < 0xA0:
		return class90
	default:
		return classA0
	}
}

// Compute returns the stat of a non-empty chunk.
func Compute(data []byte) Stat {
	if len(data) == 0 {
		panic("chunks must be non-empty")
	}

	// Leading continuation bytes belong to a rune from a previous chunk.
	s := state{kind: kindGeneral}
	for s.leading < len(data) && isContinuation(data[s.leading]) {
		s.leading++
	}
	if s.leading > 3 {
		return Invalid
	}
	if s.leading > 0 {
		s.class = classOf(data[0])
	}
	if s.leading == len(data) {
		s.kind = kindCont
		return s.encode()
	}

	// Validate the remainder, starting in the accept state.
	s.mask = maskAny
	pendingNewline := false
	for _, b := range data[s.leading:] {
		// Only count a newline once it turns out not to be trailing.
		if pendingNewline {
			s.lineBreaks = addLineBreaks(s.lineBreaks, 1)
			pendingNewline = false
		}
		if s.missing > 0 {
			if !isContinuation(b) || s.mask&(1<<classOf(b)) == 0 {
				return Invalid
			}
			s.missing, s.mask = s.missing-1, maskAny
			continue
		}
		switch {
		case b == '\n':
			pendingNewline = true
		case b == 0x00:
			// Valid UTF-8, but a strong indicator of binary data
			// (e.g., UTF-16 text). Treated as invalid, like Git does.
			return Invalid
		case b < 0x80:
		case b >= 0xC2 && b <= 0xDF:
			s.missing = 1
		case b == 0xE0:
			s.missing, s.mask = 2, 1<<classA0 // No overlong encodings.
		case b == 0xED:
			s.missing, s.mask = 2, 1<<class80|1<<class90 // No surrogates.
		case b >= 0xE1 && b <= 0xEF:
			s.missing = 2
		case b == 0xF0:
			s.missing, s.mask = 3, 1<<class90|1<<classA0 // No overlong encodings.
		case b >= 0xF1 && b <= 0xF3:
			s.missing = 3
		case b == 0xF4:
			s.missing, s.mask = 3, 1<<class80 // Nothing above U+10FFFF.
		default:
			return Invalid // Stray continuation byte, C0, C1, F5-FF.
		}
	}
	s.trailingNewline = pendingNewline
	return s.encode()
}

// Combine returns the stat of a's chunk immediately followed by b's chunk.
func Combine(av, bv Stat) Stat {
	if av == Invalid || bv == Invalid {
		return Invalid
	}
	// Fast path for the common case: both chunks start and end on rune
	// boundaries, so only the newline counts need to be added.
	if av&bv&1 != 0 {
		// a's trailing newline is no longer trailing.
		n := addLineBreaks(uint64(av)>>2+uint64(av>>1&1), uint64(bv)>>2)
		return Stat(n<<2 | uint64(bv&2) | 1)
	}

	a, b := av.decode(), bv.decode()
	switch {
	case a.kind == kindCont && b.kind == kindCont:
		// Both all-continuation: just more leading bytes.
		a.leading += b.leading
		if a.leading > 3 {
			return Invalid
		}
		return a.encode()

	case a.kind == kindCont:
		// a's bytes extend b's leading continuation bytes.
		b.leading += a.leading
		b.class = a.class
		if b.leading > 3 {
			return Invalid
		}
		return b.encode()

	case b.kind == kindCont:
		// b's bytes continue the rune left open at the end of a.
		if b.leading > a.missing || a.mask&(1<<b.class) == 0 {
			return Invalid
		}
		a.missing, a.mask = a.missing-b.leading, maskAny
		a.trailingNewline = false // Last byte is a continuation byte.
		return a.encode()

	default:
		// b's leading bytes must exactly complete a's trailing rune.
		if b.leading != a.missing ||
			(b.leading > 0 && a.mask&(1<<b.class) == 0) {
			return Invalid
		}
		a.missing, a.mask = b.missing, b.mask
		// a's trailing newline is no longer trailing.
		a.lineBreaks = addLineBreaks(
			a.lineBreaks+b2u(a.trailingNewline), b.lineBreaks,
		)
		a.trailingNewline = b.trailingNewline
		return a.encode()
	}
}
