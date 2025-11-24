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

func NewHTTPFetcher(client *http.Client) Fetcher {
	return &httpFetcher{
		client: client,
	}
}

func (f *httpFetcher) Fetch(ctx context.Context, url string, headers []*model_fetch_pb.Target_Header) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to create HTTP request")
	}
	for _, entry := range headers {
		req.Header.Set(entry.Name, entry.Value)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, util.StatusWrapfWithCode(err, codes.Internal, "Failed to download file %#v", url)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, nil
	case http.StatusNotFound:
		return nil, status.Error(codes.NotFound, "File not found")
	default:
		return nil, status.Errorf(codes.Internal, "Received unexpected HTTP response %#v", resp.Status)
	}
}
