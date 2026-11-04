package unpack

import (
	"fmt"
	"net/url"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type urlUnpackerInto struct{}

// URL is capable of unpacking Starlark strings containing URLs to a Go
// native URL.
var URL UnpackerInto[*url.URL] = urlUnpackerInto{}

func (urlUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst **url.URL) error {
	s, ok, err := eval.AsString(thread, v)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("got %s, want string", v.Type())
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	*dst = u
	return nil
}

func (ui urlUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var u *url.URL
	if err := ui.UnpackInto(thread, v, &u); err != nil {
		return nil, err
	}
	return eval.String(u.String()), nil
}

func (urlUnpackerInto) GetConcatenationOperator() syntax.Token {
	return 0
}
