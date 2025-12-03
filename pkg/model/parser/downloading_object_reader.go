package parser

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/storage/object"
)

type downloadingObjectReader[TReference any] struct {
	downloader object.Downloader[TReference]
}

// NewDownloadingObjectReader creates a ObjectReader that
// reads objects by downloading them from storage. Storage can either be
// local (e.g., backed by a disk) or remote (e.g., via gRPC).
func NewDownloadingObjectReader[TReference any](downloader object.Downloader[TReference]) ObjectReader[TReference, model_core.Message[[]byte, object.LocalReference]] {
	return &downloadingObjectReader[TReference]{
		downloader: downloader,
	}
}

func (r *downloadingObjectReader[TReference]) ReadObject(ctx context.Context, reference TReference) (model_core.Message[[]byte, object.LocalReference], error) {
	contents, err := r.downloader.DownloadObject(ctx, reference)
	if err != nil {
		return model_core.Message[[]byte, object.LocalReference]{}, err
	}
	return model_core.NewMessage(contents.GetPayload(), contents), nil
}
