package grpc

import (
	"context"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
)

type downloader struct {
	client object_pb.DownloaderClient
}

// NewDownloader creates an object downloader that forwards all requests
// to fetch objects to a remote server using gRPC.
func NewDownloader(client object_pb.DownloaderClient) object.Downloader[object.GlobalReference] {
	return &downloader{
		client: client,
	}
}

func (d *downloader) DownloadObject(ctx context.Context, reference object.GlobalReference) (*object.Contents, error) {
	response, err := d.client.DownloadObject(ctx, &object_pb.DownloadObjectRequest{
		Namespace: reference.GetNamespace().ToProto(),
		Reference: reference.GetRawReference(),
	})
	if err != nil {
		return nil, err
	}
	contents, err := object.NewContentsFromFullData(reference.LocalReference, response.Contents)
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.Internal, "Server returned invalid object contents")
	}
	return contents, nil
}
