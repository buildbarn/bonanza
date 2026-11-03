package eval

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Methods of strings. As in Bazel, strings are sequences of bytes.
// Case conversion and character classification only consider ASCII
// characters, so that bytes belonging to multi-byte UTF-8 sequences are
// never altered.

var stringMethods = map[string]*Builtin{
	"capitalize":   NewBuiltin("capitalize", stringCapitalize),
	"count":        NewBuiltin("count", stringCount),
	"elems":        NewBuiltin("elems", stringElems),
	"endswith":     NewBuiltin("endswith", stringEndswith),
	"find":         NewBuiltin("find", stringFind),
	"format":       NewBuiltin("format", stringFormat),
	"index":        NewBuiltin("index", stringIndex),
	"isalnum":      NewBuiltin("isalnum", stringIsalnum),
	"isalpha":      NewBuiltin("isalpha", stringIsalpha),
	"isdigit":      NewBuiltin("isdigit", stringIsdigit),
	"islower":      NewBuiltin("islower", stringIslower),
	"isspace":      NewBuiltin("isspace", stringIsspace),
	"istitle":      NewBuiltin("istitle", stringIstitle),
	"isupper":      NewBuiltin("isupper", stringIsupper),
	"join":         NewBuiltin("join", stringJoin),
	"lower":        NewBuiltin("lower", stringLower),
	"lstrip":       NewBuiltin("lstrip", stringLstrip),
	"partition":    NewBuiltin("partition", stringPartition),
	"removeprefix": NewBuiltin("removeprefix", stringRemoveprefix),
	"removesuffix": NewBuiltin("removesuffix", stringRemovesuffix),
	"replace":      NewBuiltin("replace", stringReplace),
	"rfind":        NewBuiltin("rfind", stringRfind),
	"rindex":       NewBuiltin("rindex", stringRindex),
	"rpartition":   NewBuiltin("rpartition", stringRpartition),
	"rsplit":       NewBuiltin("rsplit", stringRsplit),
	"rstrip":       NewBuiltin("rstrip", stringRstrip),
	"split":        NewBuiltin("split", stringSplit),
	"splitlines":   NewBuiltin("splitlines", stringSplitlines),
	"startswith":   NewBuiltin("startswith", stringStartswith),
	"strip":        NewBuiltin("strip", stringStrip),
	"title":        NewBuiltin("title", stringTitle),
	"upper":        NewBuiltin("upper", stringUpper),
}

const asciiWhitespace = " \t\n\v\f\r"

func receiverString(b *Builtin) string {
	return string(b.Receiver().(String))
}

func isASCIILower(c byte) bool { return 'a' <= c && c <= 'z' }
func isASCIIUpper(c byte) bool { return 'A' <= c && c <= 'Z' }
func isASCIIAlpha(c byte) bool { return isASCIILower(c) || isASCIIUpper(c) }
func isASCIIDigit(c byte) bool { return '0' <= c && c <= '9' }

func toASCIILower(s string) string {
	buf := []byte(s)
	for i, c := range buf {
		if isASCIIUpper(c) {
			buf[i] = c + 'a' - 'A'
		}
	}
	return string(buf)
}

func toASCIIUpper(s string) string {
	buf := []byte(s)
	for i, c := range buf {
		if isASCIILower(c) {
			buf[i] = c - 'a' + 'A'
		}
	}
	return string(buf)
}

// sliceIndices converts the optional start and end arguments of string
// methods to byte offsets, using the same semantics as slicing.
func sliceIndices(start, end Value, n int) (int, int, error) {
	convert := func(v Value, defaultValue int) (int, error) {
		if v == nil || v == Value(None) {
			return defaultValue, nil
		}
		i, err := AsInt[int](v)
		if err != nil {
			return 0, err
		}
		if i < 0 {
			i += n
		}
		return min(max(i, 0), n), nil
	}
	s, err := convert(start, 0)
	if err != nil {
		return 0, 0, err
	}
	e, err := convert(end, n)
	if err != nil {
		return 0, 0, err
	}
	return s, e, nil
}

func stringCapitalize(_ *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	s := receiverString(b)
	if s == "" {
		return String(""), nil
	}
	return String(toASCIIUpper(s[:1]) + toASCIILower(s[1:])), nil
}

func stringCount(_ *Thread, b *Builtin, args Args) (Value, error) {
	var sub String
	var start, end Value
	if err := unpackArgs(b.Name(), args, "sub", &sub, "start?", &start, "end?", &end); err != nil {
		return nil, err
	}
	s := receiverString(b)
	i, j, err := sliceIndices(start, end, len(s))
	if err != nil {
		return nil, err
	}
	if i > j {
		return MakeInt(0), nil
	}
	if sub == "" {
		return MakeInt(j - i + 1), nil
	}
	return MakeInt(strings.Count(s[i:j], string(sub))), nil
}

func stringElems(_ *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	s := receiverString(b)
	elements := make([]Value, 0, len(s))
	for i := 0; i < len(s); i++ {
		elements = append(elements, String(s[i:i+1]))
	}
	return NewList(elements), nil
}

// affixArgs returns the candidates provided to startswith() or
// endswith(), which may either be a string or a tuple of strings.
func affixArgs(t *Thread, b *Builtin, x Value) ([]string, error) {
	switch v := x.(type) {
	case String:
		return []string{string(v)}, nil
	case Tuple:
		elements, err := Elements(t, v)
		if err != nil {
			return nil, err
		}
		affixes := make([]string, 0, len(elements))
		for _, element := range elements {
			s, ok := element.(String)
			if !ok {
				return nil, fmt.Errorf("%s(): got %s in tuple, want string", b.Name(), element.Type())
			}
			affixes = append(affixes, string(s))
		}
		return affixes, nil
	}
	return nil, fmt.Errorf("%s(): got %s, want string or tuple of strings", b.Name(), x.Type())
}

func stringStartsOrEndsWith(t *Thread, b *Builtin, args Args, f func(string, string) bool) (Value, error) {
	var x, start, end Value
	if err := unpackArgs(b.Name(), args, "sub", &x, "start?", &start, "end?", &end); err != nil {
		return nil, err
	}
	affixes, err := affixArgs(t, b, x)
	if err != nil {
		return nil, err
	}
	s := receiverString(b)
	i, j, err := sliceIndices(start, end, len(s))
	if err != nil {
		return nil, err
	}
	if i > j {
		return False, nil
	}
	for _, affix := range affixes {
		if f(s[i:j], affix) {
			return True, nil
		}
	}
	return False, nil
}

func stringEndswith(t *Thread, b *Builtin, args Args) (Value, error) {
	return stringStartsOrEndsWith(t, b, args, strings.HasSuffix)
}

func stringStartswith(t *Thread, b *Builtin, args Args) (Value, error) {
	return stringStartsOrEndsWith(t, b, args, strings.HasPrefix)
}

// stringFind implements find(), index(), rfind() and rindex().
func stringFind(t *Thread, b *Builtin, args Args) (Value, error) {
	return stringFindImpl(b, args, false, false)
}

func stringIndex(t *Thread, b *Builtin, args Args) (Value, error) {
	return stringFindImpl(b, args, false, true)
}

func stringRfind(t *Thread, b *Builtin, args Args) (Value, error) {
	return stringFindImpl(b, args, true, false)
}

func stringRindex(t *Thread, b *Builtin, args Args) (Value, error) {
	return stringFindImpl(b, args, true, true)
}

func stringFindImpl(b *Builtin, args Args, last, mustExist bool) (Value, error) {
	var sub String
	var start, end Value
	if err := unpackArgs(b.Name(), args, "sub", &sub, "start?", &start, "end?", &end); err != nil {
		return nil, err
	}
	s := receiverString(b)
	i, j, err := sliceIndices(start, end, len(s))
	if err != nil {
		return nil, err
	}
	index := -1
	if i <= j {
		if last {
			index = strings.LastIndex(s[i:j], string(sub))
		} else {
			index = strings.Index(s[i:j], string(sub))
		}
	}
	if index < 0 {
		if mustExist {
			return nil, errors.New("substring not found")
		}
		return MakeInt(-1), nil
	}
	return MakeInt(i + index), nil
}

// stringFormat implements str.format(). Replacement fields may be
// empty ("{}"), contain an index ("{0}") or a keyword argument name
// ("{name}"), optionally followed by a conversion ("!s" or "!r").
// Format specifications are not supported.
func stringFormat(t *Thread, b *Builtin, args Args) (Value, error) {
	format := receiverString(b)
	var sb strings.Builder
	automatic, manual := 0, false
	for i := 0; i < len(format); {
		c := format[i]
		switch c {
		case '{':
			if strings.HasPrefix(format[i:], "{{") {
				sb.WriteByte('{')
				i += 2
				continue
			}
			end := strings.IndexByte(format[i:], '}')
			if end < 0 {
				return nil, errors.New("found '{' without matching '}'")
			}
			field := format[i+1 : i+end]
			i += end + 1

			conversion := byte('s')
			if name, conv, ok := strings.Cut(field, "!"); ok {
				if conv != "s" && conv != "r" {
					return nil, fmt.Errorf("unknown conversion %s", strconv.Quote(conv))
				}
				field, conversion = name, conv[0]
			}
			if strings.ContainsAny(field, ":{[.") {
				return nil, fmt.Errorf("unsupported replacement field %s", strconv.Quote(field))
			}

			var arg Value
			if field == "" {
				if manual {
					return nil, errors.New("cannot switch from manual field specification to automatic field numbering")
				}
				if automatic >= len(args.Positional) {
					return nil, fmt.Errorf("no replacement found for index %d", automatic)
				}
				arg = args.Positional[automatic]
				automatic++
			} else if isDecimal(field) {
				index, err := strconv.Atoi(field)
				if err != nil {
					return nil, fmt.Errorf("no replacement found for index %s", field)
				}
				if automatic > 0 {
					return nil, errors.New("cannot switch from automatic field numbering to manual field specification")
				}
				manual = true
				if index < 0 || index >= len(args.Positional) {
					return nil, fmt.Errorf("no replacement found for index %d", index)
				}
				arg = args.Positional[index]
			} else {
				for _, named := range args.Named {
					if named.Name == field {
						arg = named.Value
					}
				}
				if arg == nil {
					return nil, fmt.Errorf("missing argument '%s'", field)
				}
			}

			var s string
			var err error
			if conversion == 'r' {
				s, err = Repr(t, arg)
			} else {
				s, err = Str(t, arg)
			}
			if err != nil {
				return nil, err
			}
			sb.WriteString(s)
		case '}':
			if !strings.HasPrefix(format[i:], "}}") {
				return nil, errors.New("found '}' without matching '{'")
			}
			sb.WriteByte('}')
			i += 2
		default:
			sb.WriteByte(c)
			i++
		}
	}
	return String(sb.String()), nil
}

func isDecimal(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isASCIIDigit(s[i]) {
			return false
		}
	}
	return s != ""
}

// stringIsClass implements the is*() methods that check that the
// string is non-empty and all bytes belong to a class.
func stringIsClass(b *Builtin, args Args, f func(byte) bool) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	s := receiverString(b)
	if s == "" {
		return False, nil
	}
	for i := 0; i < len(s); i++ {
		if !f(s[i]) {
			return False, nil
		}
	}
	return True, nil
}

func stringIsalnum(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringIsClass(b, args, func(c byte) bool { return isASCIIAlpha(c) || isASCIIDigit(c) })
}

func stringIsalpha(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringIsClass(b, args, isASCIIAlpha)
}

func stringIsdigit(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringIsClass(b, args, isASCIIDigit)
}

func stringIsspace(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringIsClass(b, args, func(c byte) bool { return strings.IndexByte(asciiWhitespace, c) >= 0 })
}

// stringIsCase implements islower() and isupper(), which require at
// least one cased character, and no characters of the opposite case.
func stringIsCase(b *Builtin, args Args, isCase, isOpposite func(byte) bool) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	s := receiverString(b)
	cased := false
	for i := 0; i < len(s); i++ {
		if isOpposite(s[i]) {
			return False, nil
		}
		cased = cased || isCase(s[i])
	}
	return Bool(cased), nil
}

func stringIslower(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringIsCase(b, args, isASCIILower, isASCIIUpper)
}

func stringIsupper(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringIsCase(b, args, isASCIIUpper, isASCIILower)
}

func stringIstitle(_ *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	s := receiverString(b)
	cased, previousCased := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case isASCIIUpper(c):
			if previousCased {
				return False, nil
			}
			cased, previousCased = true, true
		case isASCIILower(c):
			if !previousCased {
				return False, nil
			}
			cased, previousCased = true, true
		default:
			previousCased = false
		}
	}
	return Bool(cased), nil
}

func stringJoin(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := unpackArgs(b.Name(), args, "elements", &x); err != nil {
		return nil, err
	}
	elements, err := Elements(t, x)
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(elements))
	for i, element := range elements {
		s, ok := element.(String)
		if !ok {
			return nil, fmt.Errorf("join(): expected string for sequence element %d, got '%s'", i, element.Type())
		}
		parts = append(parts, string(s))
	}
	return String(strings.Join(parts, receiverString(b))), nil
}

func stringLower(_ *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	return String(toASCIILower(receiverString(b))), nil
}

func stringUpper(_ *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	return String(toASCIIUpper(receiverString(b))), nil
}

func stringTitle(_ *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	buf := []byte(receiverString(b))
	previousCased := false
	for i, c := range buf {
		switch {
		case isASCIIAlpha(c):
			if previousCased {
				buf[i] = toASCIILower(string(c))[0]
			} else {
				buf[i] = toASCIIUpper(string(c))[0]
			}
			previousCased = true
		default:
			previousCased = false
		}
	}
	return String(buf), nil
}

// stringStripImpl implements strip(), lstrip() and rstrip().
func stringStripImpl(b *Builtin, args Args, left, right bool) (Value, error) {
	var chars Value
	if err := unpackArgs(b.Name(), args, "chars??", &chars); err != nil {
		return nil, err
	}
	cutset := asciiWhitespace
	if chars != nil {
		s, ok := chars.(String)
		if !ok {
			return nil, fmt.Errorf("%s(): for parameter chars: got %s, want string", b.Name(), chars.Type())
		}
		cutset = string(s)
	}
	s := receiverString(b)
	contains := func(c byte) bool { return strings.IndexByte(cutset, c) >= 0 }
	start, end := 0, len(s)
	if left {
		for start < end && contains(s[start]) {
			start++
		}
	}
	if right {
		for end > start && contains(s[end-1]) {
			end--
		}
	}
	return String(s[start:end]), nil
}

func stringStrip(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringStripImpl(b, args, true, true)
}

func stringLstrip(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringStripImpl(b, args, true, false)
}

func stringRstrip(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringStripImpl(b, args, false, true)
}

func stringPartitionImpl(b *Builtin, args Args, last bool) (Value, error) {
	var sep String
	if err := unpackArgs(b.Name(), args, "sep", &sep); err != nil {
		return nil, err
	}
	if sep == "" {
		return nil, errors.New("empty separator")
	}
	s := receiverString(b)
	var i int
	if last {
		i = strings.LastIndex(s, string(sep))
	} else {
		i = strings.Index(s, string(sep))
	}
	if i < 0 {
		if last {
			return NewTuple([]Value{String(""), String(""), String(s)}), nil
		}
		return NewTuple([]Value{String(s), String(""), String("")}), nil
	}
	return NewTuple([]Value{String(s[:i]), sep, String(s[i+len(sep):])}), nil
}

func stringPartition(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringPartitionImpl(b, args, false)
}

func stringRpartition(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringPartitionImpl(b, args, true)
}

func stringRemoveprefix(_ *Thread, b *Builtin, args Args) (Value, error) {
	var prefix String
	if err := unpackArgs(b.Name(), args, "prefix", &prefix); err != nil {
		return nil, err
	}
	return String(strings.TrimPrefix(receiverString(b), string(prefix))), nil
}

func stringRemovesuffix(_ *Thread, b *Builtin, args Args) (Value, error) {
	var suffix String
	if err := unpackArgs(b.Name(), args, "suffix", &suffix); err != nil {
		return nil, err
	}
	return String(strings.TrimSuffix(receiverString(b), string(suffix))), nil
}

func stringReplace(_ *Thread, b *Builtin, args Args) (Value, error) {
	var oldValue, newValue String
	count := -1
	if err := unpackArgs(b.Name(), args, "old", &oldValue, "new", &newValue, "count?", &count); err != nil {
		return nil, err
	}
	s := receiverString(b)
	if count < 0 {
		count = len(s) + 1
	}
	var sb strings.Builder
	if oldValue == "" {
		// An empty old string matches between every pair of
		// bytes. Unlike strings.Replace(), this does not take
		// UTF-8 sequences into account.
		for i := 0; i <= len(s); i++ {
			if count > 0 {
				sb.WriteString(string(newValue))
				count--
			}
			if i < len(s) {
				sb.WriteByte(s[i])
			}
		}
		return String(sb.String()), nil
	}
	return String(strings.Replace(s, string(oldValue), string(newValue), count)), nil
}

// stringSplitImpl implements split() and rsplit().
func stringSplitImpl(b *Builtin, args Args, fromRight bool) (Value, error) {
	var sep Value
	maxSplit := -1
	if err := unpackArgs(b.Name(), args, "sep??", &sep, "maxsplit??", &maxSplit); err != nil {
		return nil, err
	}
	s := receiverString(b)
	var parts []string
	if sep == nil {
		parts = splitWhitespace(s, maxSplit, fromRight)
	} else {
		sepString, ok := sep.(String)
		if !ok {
			return nil, fmt.Errorf("%s(): for parameter sep: got %s, want string", b.Name(), sep.Type())
		}
		if sepString == "" {
			return nil, errors.New("empty separator")
		}
		switch {
		case maxSplit < 0:
			parts = strings.Split(s, string(sepString))
		case fromRight:
			parts = rsplitN(s, string(sepString), maxSplit+1)
		default:
			parts = strings.SplitN(s, string(sepString), maxSplit+1)
		}
	}
	elements := make([]Value, 0, len(parts))
	for _, part := range parts {
		elements = append(elements, String(part))
	}
	return NewList(elements), nil
}

// rsplitN is like strings.SplitN(), except that it splits starting at
// the end of the string.
func rsplitN(s, sep string, n int) []string {
	var parts []string
	for len(parts) < n-1 {
		i := strings.LastIndex(s, sep)
		if i < 0 {
			break
		}
		parts = append(parts, s[i+len(sep):])
		s = s[:i]
	}
	parts = append(parts, s)
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return parts
}

// splitWhitespace splits a string on runs of ASCII whitespace,
// discarding leading and trailing whitespace.
func splitWhitespace(s string, maxSplit int, fromRight bool) []string {
	isSpace := func(c byte) bool { return strings.IndexByte(asciiWhitespace, c) >= 0 }
	var parts []string
	if !fromRight {
		i := 0
		for {
			for i < len(s) && isSpace(s[i]) {
				i++
			}
			if i == len(s) {
				return parts
			}
			if maxSplit >= 0 && len(parts) == maxSplit {
				return append(parts, s[i:])
			}
			j := i
			for j < len(s) && !isSpace(s[j]) {
				j++
			}
			parts = append(parts, s[i:j])
			i = j
		}
	}
	j := len(s)
	for {
		for j > 0 && isSpace(s[j-1]) {
			j--
		}
		if j == 0 {
			break
		}
		if maxSplit >= 0 && len(parts) == maxSplit {
			parts = append(parts, s[:j])
			break
		}
		i := j
		for i > 0 && !isSpace(s[i-1]) {
			i--
		}
		parts = append(parts, s[i:j])
		j = i
	}
	for i, k := 0, len(parts)-1; i < k; i, k = i+1, k-1 {
		parts[i], parts[k] = parts[k], parts[i]
	}
	return parts
}

func stringSplit(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringSplitImpl(b, args, false)
}

func stringRsplit(_ *Thread, b *Builtin, args Args) (Value, error) {
	return stringSplitImpl(b, args, true)
}

func stringSplitlines(_ *Thread, b *Builtin, args Args) (Value, error) {
	keepEnds := false
	if err := unpackArgs(b.Name(), args, "keepends?", &keepEnds); err != nil {
		return nil, err
	}
	s := receiverString(b)
	var elements []Value
	for start := 0; start < len(s); {
		end := start
		for end < len(s) && s[end] != '\n' && s[end] != '\r' {
			end++
		}
		next := end
		if next < len(s) {
			if s[next] == '\r' && next+1 < len(s) && s[next+1] == '\n' {
				next += 2
			} else {
				next++
			}
		}
		if keepEnds {
			elements = append(elements, String(s[start:next]))
		} else {
			elements = append(elements, String(s[start:end]))
		}
		start = next
	}
	return NewList(elements), nil
}
