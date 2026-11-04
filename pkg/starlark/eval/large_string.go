package eval

import (
	"bytes"
	"errors"
	"fmt"
	"hash/maphash"
	"unicode/utf8"

	"bonanza.build/pkg/starlark/syntax"
)

// LargeStringSource provides access to the contents of a string that
// is too large to be kept in memory, such as a string that is backed by
// storage.
type LargeStringSource interface {
	// SizeBytes returns the length of the string in bytes.
	SizeBytes() int64

	// ReadAt fills p with the bytes of the string starting at the
	// provided offset. The range MUST reside within the bounds of
	// the string.
	ReadAt(t *Thread, p []byte, off int64) error
}

// LargeStringSourceComparer may optionally be implemented by
// implementations of LargeStringSource that can determine whether two
// strings are equal without reading them, e.g. because they refer to
// the same object in storage.
type LargeStringSourceComparer interface {
	LargeStringSource

	// EqualSource returns true if both sources are known to
	// contain the same data.
	EqualSource(other LargeStringSource) bool
}

// LargeString is a Starlark string whose contents are provided by a
// LargeStringSource. It has type "string", and behaves identically to
// String. Operations like len(), truth, ==, ordering, hashing,
// indexing, slicing, "in", startswith() and endswith() don't require
// the string to be read into memory as a whole. All other operations
// convert the string to a String first, which is subject to a limit
// that is set per thread (see Thread.MaximumLargeStringConversionBytes).
type LargeString struct {
	source LargeStringSource
}

var (
	_ Comparable = (*LargeString)(nil)
	_ Hashable   = (*LargeString)(nil)
	_ Indexable  = (*LargeString)(nil)
	_ Sliceable  = (*LargeString)(nil)
)

// NewLargeString creates a Starlark string whose contents are provided
// by a LargeStringSource.
func NewLargeString(source LargeStringSource) *LargeString {
	return &LargeString{source: source}
}

// Source returns the LargeStringSource that provides the contents of
// the string.
func (s *LargeString) Source() LargeStringSource {
	return s.source
}

// Type returns "string".
func (*LargeString) Type() string { return "string" }

// Truth returns whether the string is non-empty.
func (s *LargeString) Truth(*Thread) (bool, error) { return s.source.SizeBytes() > 0, nil }

// largeStringReadSizeBytes is the number of bytes that is read at once
// when streaming the contents of a LargeString.
const largeStringReadSizeBytes = 1 << 16

// visitChunks calls a function for consecutive chunks of the string,
// starting at a given offset and ending at a given offset.
func (s *LargeString) visitChunks(t *Thread, start, end int64, visitor func(chunk []byte) (bool, error)) error {
	buf := make([]byte, min(end-start, largeStringReadSizeBytes))
	for off := start; off < end; {
		chunk := buf[:min(end-off, int64(len(buf)))]
		if err := s.source.ReadAt(t, chunk, off); err != nil {
			return err
		}
		if cont, err := visitor(chunk); err != nil || !cont {
			return err
		}
		off += int64(len(chunk))
	}
	return nil
}

// Repr appends the string as a quoted string literal to the printer.
func (s *LargeString) Repr(t *Thread, p *Printer) error {
	p.WriteString(`"`)
	var pending []byte
	var quoted []byte
	if err := s.visitChunks(t, 0, s.source.SizeBytes(), func(chunk []byte) (bool, error) {
		pending = append(pending, chunk...)
		var n int
		quoted, n = syntax.AppendQuotedBody(quoted[:0], pending, false)
		p.WriteString(string(quoted))
		pending = append(pending[:0], pending[n:]...)
		return true, nil
	}); err != nil {
		return err
	}
	quoted, _ = syntax.AppendQuotedBody(quoted[:0], pending, true)
	p.WriteString(string(quoted))
	p.WriteString(`"`)
	return nil
}

// Hash returns a hash of the string. It is identical to the hash of a
// String having the same contents.
func (s *LargeString) Hash(t *Thread) (uint32, error) {
	var h maphash.Hash
	h.SetSeed(hashSeed)
	if err := s.visitChunks(t, 0, s.source.SizeBytes(), func(chunk []byte) (bool, error) {
		h.Write(chunk)
		return true, nil
	}); err != nil {
		return 0, err
	}
	return uint32(h.Sum64()), nil
}

// CompareSameType compares strings lexicographically by byte.
func (s *LargeString) CompareSameType(t *Thread, op syntax.Token, y Value, _ int) (bool, error) {
	if op == syntax.EQL || op == syntax.NEQ {
		equal, err := equalStrings(t, s, y)
		return equal == (op == syntax.EQL), err
	}
	cmp, err := compareStrings(t, s, y)
	return threeway(op, cmp), err
}

// Len returns the length of the string in bytes.
func (s *LargeString) Len(*Thread) (int, error) {
	return int(s.source.SizeBytes()), nil
}

// Index returns a string containing the byte at the provided index.
func (s *LargeString) Index(t *Thread, i int) (Value, error) {
	var b [1]byte
	if err := s.source.ReadAt(t, b[:], int64(i)); err != nil {
		return nil, err
	}
	return String(b[:]), nil
}

// Slice returns a substring. Only the requested range of the string is
// read.
func (s *LargeString) Slice(t *Thread, start, end, step int) (Value, error) {
	if step == 1 {
		if start >= end {
			return String(""), nil
		}
		buf := make([]byte, end-start)
		if err := s.source.ReadAt(t, buf, int64(start)); err != nil {
			return nil, err
		}
		return String(buf), nil
	}

	// Read the range of the string that is covered by the slice,
	// and pick the requested bytes from it.
	lo, hi := start, end
	if step < 0 {
		lo, hi = end+1, start+1
	}
	if lo >= hi {
		return String(""), nil
	}
	buf := make([]byte, hi-lo)
	if err := s.source.ReadAt(t, buf, int64(lo)); err != nil {
		return nil, err
	}
	return String(buf).Slice(t, start-lo, end-lo, step)
}

// ToString converts the string to an in-memory String. The number of
// bytes that may be converted this way is limited per thread.
func (s *LargeString) ToString(t *Thread) (String, error) {
	sizeBytes := s.source.SizeBytes()
	if err := t.chargeLargeStringConversion(sizeBytes); err != nil {
		return "", err
	}
	buf := make([]byte, sizeBytes)
	if err := s.source.ReadAt(t, buf, 0); err != nil {
		return "", err
	}
	return String(buf), nil
}

func (t *Thread) chargeLargeStringConversion(sizeBytes int64) error {
	limit := t.MaximumLargeStringConversionBytes
	if sizeBytes > limit-t.largeStringConversionBytes {
		return fmt.Errorf("converting a string of %d bytes would exceed the limit of %d bytes of large strings that may be loaded into memory, as %d bytes have already been loaded", sizeBytes, limit, t.largeStringConversionBytes)
	}
	t.largeStringConversionBytes += sizeBytes
	return nil
}

// stringContents returns the contents of a Starlark string, which may
// contain arbitrary bytes. If the value is a LargeString, it is
// converted, which is subject to the limit that is set on the thread.
// If the value is not a string, ok is false.
func stringContents(t *Thread, v Value) (s string, ok bool, err error) {
	switch typedV := v.(type) {
	case String:
		return string(typedV), true, nil
	case *LargeString:
		s, err := typedV.ToString(t)
		return string(s), true, err
	default:
		return "", false, nil
	}
}

// AsString returns the contents of a Starlark string as a Go string.
// As Go strings are expected to contain UTF-8, an error is returned if
// the Starlark string contains bytes that are not valid UTF-8. Code
// that needs to process arbitrary bytes should use AsBytes() instead.
//
// If the value is a LargeString, it is converted, which is subject to
// the limit that is set on the thread. If the value is not a string, ok
// is false.
func AsString(t *Thread, v Value) (s string, ok bool, err error) {
	contents, ok, err := stringContents(t, v)
	if !ok || err != nil {
		return "", ok, err
	}
	if !utf8.ValidString(contents) {
		return "", true, errors.New("string contains bytes that are not valid UTF-8")
	}
	return contents, true, nil
}

// AsBytes returns the contents of a Starlark string as a byte slice.
// Unlike AsString(), the contents may contain arbitrary bytes.
//
// If the value is a LargeString, it is converted, which is subject to
// the limit that is set on the thread. If the value is not a string, ok
// is false.
func AsBytes(t *Thread, v Value) (b []byte, ok bool, err error) {
	contents, ok, err := stringContents(t, v)
	if !ok || err != nil {
		return nil, ok, err
	}
	return []byte(contents), true, nil
}

// IsString returns true if a value is a Starlark string, either a
// String or a LargeString.
func IsString(v Value) bool {
	switch v.(type) {
	case String, *LargeString:
		return true
	default:
		return false
	}
}

// stringSizeBytes returns the length of a Starlark string in bytes.
func stringSizeBytes(v Value) int64 {
	switch typedV := v.(type) {
	case String:
		return int64(len(typedV))
	case *LargeString:
		return typedV.source.SizeBytes()
	default:
		panic("value is not a string")
	}
}

// readStringAt reads a range of bytes from a Starlark string.
func readStringAt(t *Thread, v Value, p []byte, off int64) error {
	switch typedV := v.(type) {
	case String:
		copy(p, typedV[off:])
		return nil
	case *LargeString:
		return typedV.source.ReadAt(t, p, off)
	default:
		panic("value is not a string")
	}
}

// compareStringRanges compares a range of two Starlark strings of equal
// length, without reading either of them into memory as a whole.
func compareStringRanges(t *Thread, x Value, xOff int64, y Value, yOff, sizeBytes int64) (int, error) {
	xBuf := make([]byte, min(sizeBytes, largeStringReadSizeBytes))
	yBuf := make([]byte, len(xBuf))
	for off := int64(0); off < sizeBytes; {
		n := min(sizeBytes-off, int64(len(xBuf)))
		if err := readStringAt(t, x, xBuf[:n], xOff+off); err != nil {
			return 0, err
		}
		if err := readStringAt(t, y, yBuf[:n], yOff+off); err != nil {
			return 0, err
		}
		if cmp := bytes.Compare(xBuf[:n], yBuf[:n]); cmp != 0 {
			return cmp, nil
		}
		off += n
	}
	return 0, nil
}

// equalStrings returns whether two Starlark strings are equal. y may
// be of any type.
func equalStrings(t *Thread, x, y Value) (bool, error) {
	if !IsString(y) {
		return false, nil
	}
	xSizeBytes := stringSizeBytes(x)
	if xSizeBytes != stringSizeBytes(y) {
		return false, nil
	}
	if xl, ok := x.(*LargeString); ok {
		if yl, ok := y.(*LargeString); ok {
			if xl == yl {
				return true, nil
			}
			if c, ok := xl.source.(LargeStringSourceComparer); ok && c.EqualSource(yl.source) {
				return true, nil
			}
		}
	}
	cmp, err := compareStringRanges(t, x, 0, y, 0, xSizeBytes)
	return cmp == 0, err
}

// compareStrings compares two Starlark strings lexicographically by
// byte.
func compareStrings(t *Thread, x, y Value) (int, error) {
	xSizeBytes, ySizeBytes := stringSizeBytes(x), stringSizeBytes(y)
	if cmp, err := compareStringRanges(t, x, 0, y, 0, min(xSizeBytes, ySizeBytes)); cmp != 0 || err != nil {
		return cmp, err
	}
	switch {
	case xSizeBytes < ySizeBytes:
		return -1, nil
	case xSizeBytes > ySizeBytes:
		return 1, nil
	default:
		return 0, nil
	}
}

// hasPrefix returns whether a LargeString starts with a given prefix.
func (s *LargeString) hasPrefix(t *Thread, prefix String) (bool, error) {
	if int64(len(prefix)) > s.source.SizeBytes() {
		return false, nil
	}
	cmp, err := compareStringRanges(t, s, 0, prefix, 0, int64(len(prefix)))
	return cmp == 0, err
}

// hasSuffix returns whether a LargeString ends with a given suffix.
func (s *LargeString) hasSuffix(t *Thread, suffix String) (bool, error) {
	sizeBytes := s.source.SizeBytes()
	if int64(len(suffix)) > sizeBytes {
		return false, nil
	}
	cmp, err := compareStringRanges(t, s, sizeBytes-int64(len(suffix)), suffix, 0, int64(len(suffix)))
	return cmp == 0, err
}

// contains returns whether a LargeString contains a substring. The
// string is read in chunks that overlap by the length of the needle,
// so that occurrences that cross chunk boundaries are found.
func (s *LargeString) contains(t *Thread, needle String) (bool, error) {
	sizeBytes := s.source.SizeBytes()
	if int64(len(needle)) > sizeBytes {
		return false, nil
	}
	if len(needle) == 0 {
		return true, nil
	}
	overlap := int64(len(needle) - 1)
	readSizeBytes := max(largeStringReadSizeBytes, 2*overlap)
	buf := make([]byte, min(sizeBytes, readSizeBytes+overlap))
	for off := int64(0); off < sizeBytes-overlap; off += int64(len(buf)) - overlap {
		chunk := buf[:min(sizeBytes-off, int64(len(buf)))]
		if err := s.source.ReadAt(t, chunk, off); err != nil {
			return false, err
		}
		if bytes.Contains(chunk, []byte(needle)) {
			return true, nil
		}
	}
	return false, nil
}

// largeStringPrefixSuffixMethod implements startswith() and
// endswith() for large strings, without reading them into memory as a
// whole.
func largeStringPrefixSuffixMethod(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	var start, end Value = None, None
	if err := UnpackArgs(t, b.Name(), args, "x", &x, "start?", &start, "end?", &end); err != nil {
		return nil, err
	}
	s := b.Receiver().(*LargeString)
	if start != Value(None) || end != Value(None) {
		// Fall back to the regular implementation.
		converted, err := s.ToString(t)
		if err != nil {
			return nil, err
		}
		return stringMethods[b.Name()].BindReceiver(converted).CallInternal(t, args)
	}

	var candidates []Value
	if tuple, ok := x.(Tuple); ok {
		var err error
		if candidates, err = Elements(t, tuple); err != nil {
			return nil, err
		}
	} else {
		candidates = []Value{x}
	}
	for _, candidate := range candidates {
		affix, ok, err := stringContents(t, candidate)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%s(): got %s, want string or tuple of strings", b.Name(), candidate.Type())
		}
		var match bool
		if b.Name() == "startswith" {
			match, err = s.hasPrefix(t, String(affix))
		} else {
			match, err = s.hasSuffix(t, String(affix))
		}
		if err != nil || match {
			return Bool(match), err
		}
	}
	return False, nil
}

var largeStringMethods = map[string]*Builtin{
	"endswith":   NewBuiltin("endswith", largeStringPrefixSuffixMethod),
	"startswith": NewBuiltin("startswith", largeStringPrefixSuffixMethod),
}

// getLargeStringAttr implements x.name for large strings. Methods that
// can operate on large strings directly are returned as is. For all
// other methods, the string is converted to a String first.
func getLargeStringAttr(t *Thread, s *LargeString, name string) (Value, error) {
	if method, ok := largeStringMethods[name]; ok {
		return method.BindReceiver(s), nil
	}
	if _, ok := stringMethods[name]; !ok {
		return nil, NoSuchAttrError{Value: s, Name: name}
	}
	converted, err := s.ToString(t)
	if err != nil {
		return nil, err
	}
	method, _ := lookupMethod(converted, name)
	return method, nil
}
