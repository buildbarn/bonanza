package fetch

import (
	"context"
	"io"

	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"
)

// Fetcher is called into by LocalExecutor to fetch files. Simple setups
// may only use a single instance of Fetcher. More complex ones can use
// multiple instances in case support for different network protocols is
// desired.
type Fetcher interface {
	// Fetch the file at a given URL. If known, the size of the
	// file is returned. Otherwise, the size is -1.
	Fetch(ctx context.Context, url string, headers []*model_fetch_pb.Target_Header) (body io.ReadCloser, sizeBytes int64, err error)
}
