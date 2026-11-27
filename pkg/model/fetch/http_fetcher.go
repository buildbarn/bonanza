package fetch

import (
	"context"
	"io"
	"net/http"

	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type httpFetcher struct {
	client *http.Client
}

// NewHTTPFetcher creates a Fetcher that is capable of fetching files
// via HTTP and HTTPS.
func NewHTTPFetcher(client *http.Client) Fetcher {
	return &httpFetcher{
		client: client,
	}
}

func (f *httpFetcher) Fetch(ctx context.Context, url string, headers []*model_fetch_pb.Target_Header) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to create HTTP request")
	}
	for _, entry := range headers {
		req.Header.Set(entry.GetName(), entry.GetValue())
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, 0, util.StatusWrapfWithCode(err, codes.Internal, "Failed to download file %#v", url)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, resp.ContentLength, nil
	case http.StatusNotFound:
		return nil, 0, status.Error(codes.NotFound, "File not found")
	default:
		return nil, 0, status.Errorf(codes.Internal, "Received unexpected HTTP response %#v", resp.Status)
	}
}
