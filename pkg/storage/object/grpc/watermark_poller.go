package grpc

import (
	"context"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/types/known/emptypb"
)

// PollWatermark periodically obtains the watermark of a storage server
// and raises the watermark of the replica the server is part of
// accordingly. Storage frontends use this to learn the highest
// watermark of each replica, which they provide to storage servers as
// part of requests that need to check leases.
//
// This function returns when the context is cancelled.
func PollWatermark(ctx context.Context, client object_pb.UploaderClient, clock clock.Clock, interval time.Duration, replicaWatermark *object.Watermark, errorLogger util.ErrorLogger) {
	for {
		if response, err := client.GetWatermark(ctx, &emptypb.Empty{}); err == nil {
			replicaWatermark.RaiseFromProto(response.Watermark)
		} else if ctx.Err() == nil {
			errorLogger.Log(util.StatusWrap(err, "Failed to obtain watermark"))
		}

		timer, t := clock.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-t:
		}
	}
}
