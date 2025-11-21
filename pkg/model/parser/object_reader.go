package parser

import (
	"context"
)

type ObjectReader[TReference, TParsedObject any] interface {
	ReadObject(ctx context.Context, reference TReference) (TParsedObject, error)
	GetDecodingParametersSizeBytes() int
}
