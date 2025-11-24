package fetch

import (
	"context"
	"io"
	"net/url"

	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type schemeDemultiplexingFetcher struct {
	fetchersByScheme map[string]Fetcher
}

// NewSchemeDemultiplexingFetcher wraps a set of Fetchers, and forwards
// requests to them based on the URL scheme. This makes it possible to
// launch a single bonanza_fetcher process that supports various URL
// schemes.
func NewSchemeDemultiplexingFetcher(fetchersByScheme map[string]Fetcher) Fetcher {
	return &schemeDemultiplexingFetcher{
		fetchersByScheme: fetchersByScheme,
	}
}

func (f *schemeDemultiplexingFetcher) Fetch(ctx context.Context, rawURL string, headers []*model_fetch_pb.Target_Header) (io.ReadCloser, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid URL")
	}
	fetcher, ok := f.fetchersByScheme[parsedURL.Scheme]
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid URL scheme %#v", parsedURL.Scheme)
	}
	return fetcher.Fetch(ctx, rawURL, headers)
}
