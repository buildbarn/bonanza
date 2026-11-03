package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// BuiltinModule is a value that provides a set of builtins as its
// attributes, such as the json module.
type BuiltinModule struct {
	name    string
	members map[string]Value
}

var _ HasAttrs = (*BuiltinModule)(nil)

// NewBuiltinModule creates a module whose attributes are the provided
// members.
func NewBuiltinModule(name string, members map[string]Value) *BuiltinModule {
	return &BuiltinModule{name: name, members: members}
}

// Type returns "module".
func (*BuiltinModule) Type() string { return "module" }

// Truth returns true.
func (*BuiltinModule) Truth(*Thread) (bool, error) { return true, nil }

// Repr appends the representation of the module to the printer.
func (m *BuiltinModule) Repr(_ *Thread, p *Printer) error {
	p.WriteString("<module " + m.name + ">")
	return nil
}

// Attr returns a member of the module.
func (m *BuiltinModule) Attr(_ *Thread, name string) (Value, error) {
	if v, ok := m.members[name]; ok {
		return v, nil
	}
	return nil, NoSuchAttrError{Value: m, Name: name}
}

// AttrNames returns the names of the members of the module.
func (m *BuiltinModule) AttrNames(*Thread) ([]string, error) {
	names := make([]string, 0, len(m.members))
	for name := range m.members {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

var jsonModule = NewBuiltinModule("json", map[string]Value{
	"decode":        NewBuiltin("json.decode", jsonDecode),
	"encode":        NewBuiltin("json.encode", jsonEncode),
	"encode_indent": NewBuiltin("json.encode_indent", jsonEncodeIndent),
	"indent":        NewBuiltin("json.indent", jsonIndent),
})

// maximumJSONDepth limits the nesting depth of values that are encoded
// as JSON, so that cycles are detected.
const maximumJSONDepth = 1000

// EncodeJSON encodes a value as JSON. Dict entries and struct fields
// are emitted in sorted order.
func EncodeJSON(t *Thread, x Value) (string, error) {
	var buf bytes.Buffer
	if err := encodeJSON(t, &buf, x, nil); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// encodeJSON encodes a value as JSON. path contains the composite
// values that are currently being encoded, which is used to detect
// cycles.
func encodeJSON(t *Thread, buf *bytes.Buffer, x Value, path []Value) error {
	if len(path) > maximumJSONDepth {
		return errors.New("nesting depth limit exceeded")
	}
	switch x.(type) {
	case Tuple:
		// Tuples are immutable, meaning they cannot be part of
		// a cycle without also containing a mutable value.
	case Dict, Iterable, HasAttrs:
		for _, v := range path {
			if sameValue(v, x) {
				return errors.New("cycle in JSON structure")
			}
		}
		path = append(path, x)
	}
	switch v := x.(type) {
	case NoneType:
		buf.WriteString("null")
	case Bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case Int:
		buf.WriteString(v.String())
	case Float:
		if math.IsInf(float64(v), 0) || math.IsNaN(float64(v)) {
			return fmt.Errorf("cannot encode non-finite float %s", v)
		}
		buf.WriteString(v.String())
	case String:
		encodeJSONString(buf, string(v))
	case Dict:
		type entry struct {
			key   string
			value Value
		}
		var entries []entry
		var errIter error
		for k, value := range v.Items(t, &errIter) {
			key, ok := k.(String)
			if !ok {
				return fmt.Errorf("dict has %s key, want string", k.Type())
			}
			entries = append(entries, entry{key: string(key), value: value})
		}
		if errIter != nil {
			return errIter
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
		buf.WriteByte('{')
		for i, e := range entries {
			if i > 0 {
				buf.WriteByte(',')
			}
			encodeJSONString(buf, e.key)
			buf.WriteByte(':')
			if err := encodeJSON(t, buf, e.value, path); err != nil {
				return fmt.Errorf("in dict key %s: %w", strconv.Quote(e.key), err)
			}
		}
		buf.WriteByte('}')
	case HasAttrs:
		names, err := v.AttrNames(t)
		if err != nil {
			return err
		}
		names = append([]string(nil), names...)
		sort.Strings(names)
		buf.WriteByte('{')
		for i, name := range names {
			value, err := v.Attr(t, name)
			if err != nil {
				return fmt.Errorf("cannot access attribute %s.%s: %w", x.Type(), name, err)
			}
			if i > 0 {
				buf.WriteByte(',')
			}
			encodeJSONString(buf, name)
			buf.WriteByte(':')
			if err := encodeJSON(t, buf, value, path); err != nil {
				return fmt.Errorf("in field .%s: %w", name, err)
			}
		}
		buf.WriteByte('}')
	case Iterable:
		elements, err := Elements(t, v)
		if err != nil {
			return err
		}
		buf.WriteByte('[')
		for i, element := range elements {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeJSON(t, buf, element, path); err != nil {
				return fmt.Errorf("at %s index %d: %w", x.Type(), i, err)
			}
		}
		buf.WriteByte(']')
	default:
		return fmt.Errorf("cannot encode %s as JSON", x.Type())
	}
	return nil
}

// encodeJSONString encodes a string as a JSON string literal. Bytes
// that are not part of valid UTF-8 sequences are replaced by U+FFFD.
func encodeJSONString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for len(s) > 0 {
		r, width := utf8.DecodeRuneInString(s)
		switch {
		case r == '"' || r == '\\':
			buf.WriteByte('\\')
			buf.WriteByte(byte(r))
		case r == '\n':
			buf.WriteString(`\n`)
		case r == '\r':
			buf.WriteString(`\r`)
		case r == '\t':
			buf.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(buf, `\u%04x`, r)
		case r == utf8.RuneError && width == 1:
			buf.WriteString(`\ufffd`)
		default:
			buf.WriteString(s[:width])
		}
		s = s[width:]
	}
	buf.WriteByte('"')
}

func jsonEncode(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "x", &x); err != nil {
		return nil, err
	}
	s, err := EncodeJSON(t, x)
	if err != nil {
		return nil, fmt.Errorf("%s(): %w", b.Name(), err)
	}
	return String(s), nil
}

func indentJSON(s, prefix, indent string) (string, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), prefix, indent); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func jsonEncodeIndent(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	var prefix, indent String = "", "\t"
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("%s() accepts at most 1 positional argument", b.Name())
	}
	if err := UnpackArgs(t, b.Name(), args, "x", &x, "prefix?", &prefix, "indent?", &indent); err != nil {
		return nil, err
	}
	s, err := EncodeJSON(t, x)
	if err != nil {
		return nil, fmt.Errorf("%s(): %w", b.Name(), err)
	}
	indented, err := indentJSON(s, string(prefix), string(indent))
	if err != nil {
		return nil, fmt.Errorf("%s(): %w", b.Name(), err)
	}
	return String(indented), nil
}

func jsonIndent(t *Thread, b *Builtin, args Args) (Value, error) {
	var s, prefix, indent String = "", "", "\t"
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("%s() accepts at most 1 positional argument", b.Name())
	}
	if err := UnpackArgs(t, b.Name(), args, "s", &s, "prefix?", &prefix, "indent?", &indent); err != nil {
		return nil, err
	}
	indented, err := indentJSON(string(s), string(prefix), string(indent))
	if err != nil {
		return nil, fmt.Errorf("%s(): %w", b.Name(), err)
	}
	return String(indented), nil
}

func jsonDecode(t *Thread, b *Builtin, args Args) (Value, error) {
	// The value to decode can only be provided positionally.
	for _, arg := range args.Named {
		if arg.Name == "x" {
			return nil, fmt.Errorf("%s() got unexpected keyword argument 'x'", b.Name())
		}
	}
	var s String
	var defaultValue Value
	if err := UnpackArgs(t, b.Name(), args, "x", &s, "default?", &defaultValue); err != nil {
		return nil, err
	}
	v, err := DecodeJSON(string(s))
	if err != nil {
		if defaultValue != nil {
			return defaultValue, nil
		}
		return nil, fmt.Errorf("%s(): %w", b.Name(), err)
	}
	return v, nil
}

// DecodeJSON decodes a JSON value. Numbers without a fraction or
// exponent are decoded as ints, and all other numbers as floats.
// Objects are decoded as mutable dicts, and arrays as mutable lists.
func DecodeJSON(s string) (Value, error) {
	d := jsonDecoder{s: s}
	v, err := d.value(0)
	if err != nil {
		return nil, err
	}
	d.skipSpace()
	if d.i < len(d.s) {
		return nil, d.errorf("unexpected character %s after value", strconv.QuoteRune(rune(d.s[d.i])))
	}
	return v, nil
}

type jsonDecoder struct {
	s string
	i int
}

func (d *jsonDecoder) errorf(format string, args ...any) error {
	return fmt.Errorf("at offset %d, %s", d.i, fmt.Sprintf(format, args...))
}

func (d *jsonDecoder) skipSpace() {
	for d.i < len(d.s) && strings.IndexByte(" \t\n\r", d.s[d.i]) >= 0 {
		d.i++
	}
}

func (d *jsonDecoder) value(depth int) (Value, error) {
	if depth > maximumJSONDepth {
		return nil, d.errorf("nesting depth limit exceeded")
	}
	d.skipSpace()
	if d.i == len(d.s) {
		return nil, d.errorf("unexpected end of file")
	}
	switch c := d.s[d.i]; {
	case c == 'n' && strings.HasPrefix(d.s[d.i:], "null"):
		d.i += 4
		return None, nil
	case c == 't' && strings.HasPrefix(d.s[d.i:], "true"):
		d.i += 4
		return True, nil
	case c == 'f' && strings.HasPrefix(d.s[d.i:], "false"):
		d.i += 5
		return False, nil
	case c == '"':
		s, err := d.string()
		if err != nil {
			return nil, err
		}
		return String(s), nil
	case c == '-' || ('0' <= c && c <= '9'):
		return d.number()
	case c == '[':
		d.i++
		var elements []Value
		d.skipSpace()
		if d.i < len(d.s) && d.s[d.i] == ']' {
			d.i++
			return NewList(nil), nil
		}
		for {
			element, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			elements = append(elements, element)
			d.skipSpace()
			if d.i == len(d.s) {
				return nil, d.errorf("unexpected end of file")
			}
			switch d.s[d.i] {
			case ',':
				d.i++
			case ']':
				d.i++
				return NewList(elements), nil
			default:
				return nil, d.errorf("got %s, want ',' or ']'", strconv.QuoteRune(rune(d.s[d.i])))
			}
		}
	case c == '{':
		d.i++
		dict := &memDict{}
		d.skipSpace()
		if d.i < len(d.s) && d.s[d.i] == '}' {
			d.i++
			return dict, nil
		}
		for {
			d.skipSpace()
			if d.i == len(d.s) {
				return nil, d.errorf("unexpected end of file")
			}
			if d.s[d.i] != '"' {
				return nil, d.errorf("unexpected character %s", strconv.QuoteRune(rune(d.s[d.i])))
			}
			key, err := d.string()
			if err != nil {
				return nil, err
			}
			d.skipSpace()
			if d.i == len(d.s) || d.s[d.i] != ':' {
				return nil, d.errorf("after object key, got %s, want ':'", d.peekDescription())
			}
			d.i++
			value, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			// Keys are strings, so this cannot fail.
			_ = dict.SetKey(nil, String(key), value)
			d.skipSpace()
			if d.i == len(d.s) {
				return nil, d.errorf("unexpected end of file")
			}
			switch d.s[d.i] {
			case ',':
				d.i++
			case '}':
				d.i++
				return dict, nil
			default:
				return nil, d.errorf("in object, got %s, want ',' or '}'", d.peekDescription())
			}
		}
	default:
		return nil, d.errorf("unexpected character %s", strconv.QuoteRune(rune(c)))
	}
}

func (d *jsonDecoder) peekDescription() string {
	if d.i == len(d.s) {
		return "end of file"
	}
	return strconv.QuoteRune(rune(d.s[d.i]))
}

// jsonNumberPattern matches numbers, as permitted by the JSON grammar.
var jsonNumberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func (d *jsonDecoder) number() (Value, error) {
	// Consume all characters that may be part of a number, and
	// validate the result against the grammar afterwards.
	start := d.i
	for d.i < len(d.s) && strings.IndexByte("0123456789+-.eE", d.s[d.i]) >= 0 {
		d.i++
	}
	literal := d.s[start:d.i]
	if !jsonNumberPattern.MatchString(literal) {
		return nil, d.errorf("invalid number: %s", literal)
	}
	isFloat := strings.ContainsAny(literal, ".eE")
	if isFloat {
		f, err := strconv.ParseFloat(literal, 64)
		if err != nil {
			return nil, d.errorf("invalid number %s", literal)
		}
		return Float(f), nil
	}
	i, ok := new(big.Int).SetString(literal, 10)
	if !ok {
		return nil, d.errorf("invalid number %s", literal)
	}
	return MakeBigInt(i), nil
}

func (d *jsonDecoder) string() (string, error) {
	// Skip the opening quote.
	d.i++
	var sb strings.Builder
	for {
		if d.i == len(d.s) {
			return "", d.errorf("unclosed string literal")
		}
		c := d.s[d.i]
		switch {
		case c == '"':
			d.i++
			return sb.String(), nil
		case c < 0x20:
			return "", d.errorf("control character in string")
		case c != '\\':
			sb.WriteByte(c)
			d.i++
			continue
		}

		d.i++
		if d.i == len(d.s) {
			return "", d.errorf("unclosed string literal")
		}
		escape := d.s[d.i]
		d.i++
		switch escape {
		case '"', '\\', '/':
			sb.WriteByte(escape)
		case 'b':
			sb.WriteByte('\b')
		case 'f':
			sb.WriteByte('\f')
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		case 't':
			sb.WriteByte('\t')
		case 'u':
			r, err := d.hex4()
			if err != nil {
				return "", err
			}
			if utf16.IsSurrogate(r) && strings.HasPrefix(d.s[d.i:], `\u`) {
				d.i += 2
				r2, err := d.hex4()
				if err != nil {
					return "", err
				}
				r = utf16.DecodeRune(r, r2)
			}
			sb.WriteRune(r)
		default:
			return "", d.errorf("invalid character '%c' in string escape code", escape)
		}
	}
}

func (d *jsonDecoder) hex4() (rune, error) {
	if d.i+4 > len(d.s) {
		return 0, d.errorf("incomplete \\u escape")
	}
	n, err := strconv.ParseUint(d.s[d.i:d.i+4], 16, 16)
	if err != nil {
		return 0, d.errorf("invalid \\u escape")
	}
	d.i += 4
	return rune(n), nil
}
