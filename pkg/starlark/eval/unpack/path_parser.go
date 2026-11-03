package unpack

import (
	"fmt"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type pathParserUnpackerInto struct {
	pathFormat path.Format
}

// PathParser is capable of parsing pathname strings, wrapping them in a
// path parser for traversing over its components.
func PathParser(pathFormat path.Format) UnpackerInto[path.Parser] {
	return &pathParserUnpackerInto{
		pathFormat: pathFormat,
	}
}

func (ui *pathParserUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *path.Parser) error {
	s, ok := eval.AsString(v)
	if !ok {
		return fmt.Errorf("got %s, want string", v.Type())
	}
	*dst = ui.pathFormat.NewParser(s)
	return nil
}

func (ui *pathParserUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	s, ok := eval.AsString(v)
	if !ok {
		return nil, fmt.Errorf("got %s, want string", v.Type())
	}
	builder, scopeWalker := path.EmptyBuilder.Join(path.VoidScopeWalker)
	if err := path.Resolve(ui.pathFormat.NewParser(s), scopeWalker); err != nil {
		return nil, err
	}
	s, err := ui.pathFormat.GetString(builder)
	if err != nil {
		return nil, err
	}
	return eval.String(s), nil
}

func (pathParserUnpackerInto) GetConcatenationOperator() syntax.Token {
	return 0
}
