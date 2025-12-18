package tag

import (
	"context"
	"crypto/sha256"

	model_core "bonanza.build/pkg/model/core"
)

type objectImportingBoundResolver[TInternal, TExternal any] struct {
	base           BoundResolver[TExternal]
	objectExporter model_core.ObjectExporter[TInternal, TExternal]
}

// NewObjectImportingBoundResolver creates a decorator for BoundResolver
// that converts the references it obtained from its backend to another
// format, using an ObjectExporter. This can, for example, be used to
// convert instances of object.LocalReference to ones that are buffered.
func NewObjectImportingBoundResolver[TInternal, TExternal any](base BoundResolver[TExternal], objectExporter model_core.ObjectExporter[TInternal, TExternal]) BoundResolver[TInternal] {
	return &objectImportingBoundResolver[TInternal, TExternal]{
		base:           base,
		objectExporter: objectExporter,
	}
}

func (r *objectImportingBoundResolver[TInternal, TExternal]) ResolveTag(ctx context.Context, keyHash [sha256.Size]byte) (TInternal, error) {
	externalReference, err := r.base.ResolveTag(ctx, keyHash)
	if err != nil {
		var badReference TInternal
		return badReference, err
	}
	return r.objectExporter.ImportReference(externalReference), nil
}
