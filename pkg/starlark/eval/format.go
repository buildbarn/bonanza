package eval

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// percentFormat implements the string formatting operator: format %
// args. If args is a tuple, its elements are used as the arguments.
// Named conversions, such as %(name)s, require args to be a mapping.
func percentFormat(t *Thread, format string, args Value) (Value, error) {
	var positional []Value
	if tuple, ok := args.(Tuple); ok {
		elements, err := Elements(t, tuple)
		if err != nil {
			return nil, err
		}
		positional = elements
	} else {
		positional = []Value{args}
	}

	var sb strings.Builder
	used := 0
	for i := 0; i < len(format); {
		c := format[i]
		i++
		if c != '%' {
			sb.WriteByte(c)
			continue
		}
		if i == len(format) {
			return nil, errors.New("incomplete format")
		}

		// Obtain the argument, either by name or by position.
		var arg Value
		if format[i] == '(' {
			end := strings.IndexByte(format[i:], ')')
			if end < 0 {
				return nil, errors.New("incomplete format key")
			}
			key := format[i+1 : i+end]
			i += end + 1
			mapping, ok := args.(Mapping)
			if !ok {
				return nil, errors.New("format requires a mapping")
			}
			v, found, err := mapping.Get(t, String(key))
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("key %s not found", strconv.Quote(key))
			}
			arg = v
			if i == len(format) {
				return nil, errors.New("incomplete format")
			}
		} else if format[i] != '%' {
			if used >= len(positional) {
				return nil, errors.New("not enough arguments for format string")
			}
			arg = positional[used]
			used++
		}

		conversion := format[i]
		i++
		switch conversion {
		case '%':
			sb.WriteByte('%')
		case 's', 'r':
			var s string
			var err error
			if conversion == 's' {
				s, err = Str(t, arg)
			} else {
				s, err = Repr(t, arg)
			}
			if err != nil {
				return nil, err
			}
			sb.WriteString(s)
		case 'd', 'i', 'o', 'x', 'X':
			var n Int
			switch v := arg.(type) {
			case Int:
				n = v
			case Float:
				integral, ok := truncateFloat(v)
				if !ok {
					return nil, fmt.Errorf("%%%c format requires an integer, not an infinite or NaN float", conversion)
				}
				n = integral
			default:
				return nil, fmt.Errorf("%%%c format requires integer: %s", conversion, arg.Type())
			}
			switch conversion {
			case 'd', 'i':
				sb.WriteString(n.String())
			case 'o':
				sb.WriteString(n.BigInt().Text(8))
			case 'x':
				sb.WriteString(n.BigInt().Text(16))
			case 'X':
				sb.WriteString(strings.ToUpper(n.BigInt().Text(16)))
			}
		case 'e', 'f', 'g', 'E', 'F', 'G':
			var f Float
			switch v := arg.(type) {
			case Float:
				f = v
			case Int:
				var err error
				if f, err = v.Float(); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("%%%c format requires float, not %s", conversion, arg.Type())
			}
			sb.WriteString(formatFloat(f, conversion))
		default:
			return nil, fmt.Errorf("unsupported format character '%c'", conversion)
		}
	}
	if used < len(positional) {
		if _, isMapping := args.(Mapping); !isMapping || used > 0 {
			return nil, errors.New("too many arguments for format string")
		}
	}
	return String(sb.String()), nil
}

// truncateFloat truncates a float towards zero, returning it as an
// integer. It fails for infinities and NaN.
func truncateFloat(f Float) (Int, bool) {
	return Float(math.Trunc(float64(f))).integral()
}

// formatFloat formats a float using one of the conversions supported
// by the % operator.
func formatFloat(f Float, conversion byte) string {
	switch conversion {
	case 'e', 'E':
		return strconv.FormatFloat(float64(f), conversion, 6, 64)
	case 'f', 'F':
		return strconv.FormatFloat(float64(f), 'f', 6, 64)
	default:
		s := strconv.FormatFloat(float64(f), 'g', 6, 64)
		if conversion == 'G' {
			s = strings.ToUpper(s)
		}
		return s
	}
}
