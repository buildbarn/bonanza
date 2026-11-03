package syntax_test

import (
	"fmt"
	"reflect"
	"strings"

	"bonanza.build/pkg/starlark/syntax"
)

// treeString converts a syntax tree to a compact string
// representation, which is used by the tests to compare parse trees.
// Positions and fields containing zero values are omitted.
func treeString(n any) string {
	var sb strings.Builder
	writeTree(&sb, reflect.ValueOf(n), false)
	return sb.String()
}

// treeStringWithPositions is like treeString, except that it also
// includes all positions. String literals are emitted by value instead
// of by their raw text.
func treeStringWithPositions(n any) string {
	var sb strings.Builder
	writeTree(&sb, reflect.ValueOf(n), true)
	return sb.String()
}

var positionType = reflect.TypeFor[syntax.Position]()

func writeTree(sb *strings.Builder, v reflect.Value, withPositions bool) {
	switch v.Kind() {
	case reflect.Interface:
		writeTree(sb, v.Elem(), withPositions)
		return
	case reflect.Slice:
		sb.WriteByte('(')
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				sb.WriteByte(' ')
			}
			writeTree(sb, v.Index(i), withPositions)
		}
		sb.WriteByte(')')
		return
	case reflect.Pointer:
		switch n := v.Interface().(type) {
		case *syntax.Ident:
			sb.WriteString(n.Name)
			if withPositions {
				fmt.Fprintf(sb, "@%s", n.NamePos)
			}
			return
		case *syntax.Literal:
			if withPositions {
				fmt.Fprintf(sb, "%#v@%s", n.Value, n.TokenPos)
			} else {
				sb.WriteString(n.Raw)
			}
			return
		}
		v = v.Elem()
	case reflect.String, reflect.Bool, reflect.Int8, reflect.Uint8:
		fmt.Fprint(sb, v.Interface())
		return
	}

	t := v.Type()
	fmt.Fprintf(sb, "(%s", t.Name())
	for i := 0; i < t.NumField(); i++ {
		field := v.Field(i)
		if field.IsZero() || (field.Kind() == reflect.Slice && field.Len() == 0) {
			continue
		}
		if t.Field(i).Type == positionType {
			if withPositions {
				fmt.Fprintf(sb, " %s@%s", t.Field(i).Name, field.Interface())
			}
			continue
		}
		if field.Kind() == reflect.Bool {
			fmt.Fprintf(sb, " %s", t.Field(i).Name)
			continue
		}
		fmt.Fprintf(sb, " %s=", t.Field(i).Name)
		writeTree(sb, field, withPositions)
	}
	sb.WriteByte(')')
}
