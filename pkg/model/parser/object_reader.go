package parser

import (
	"context"
)

// ObjectReader can be used to read an object from storage and return
// its contents. This is a generalization of object.Downloader, in that
// contents can also be returned in decoded and parsed form.
type ObjectReader[TReference, TParsedObject any] interface {
	ReadObject(ctx context.Context, reference TReference) (TParsedObject, error)
}
