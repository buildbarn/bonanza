package mirrored

import (
	"context"
	"time"

	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type resolver[TNamespace any] struct {
	replicaA tag.Resolver[TNamespace]
	replicaB tag.Resolver[TNamespace]
}

// NewResolver creates a decorator for tag.Resolver that forwards
// requests to resolve tags to a pair of backends that are configured to
// mirror each other's contents.
func NewResolver[TNamespace any](replicaA, replicaB tag.Resolver[TNamespace]) tag.Resolver[TNamespace] {
	return &resolver[TNamespace]{
		replicaA: replicaA,
		replicaB: replicaB,
	}
}

func (r *resolver[TNamespace]) ResolveTag(ctx context.Context, namespace TNamespace, key tag.Key, minimumTimestamp *time.Time) (value tag.SignedValue, complete bool, err error) {
	// Send request to both replicas.
	var signedValueA, signedValueB tag.SignedValue
	var completeA, completeB bool
	var foundA, foundB bool
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		signedValueA, completeA, err = r.replicaA.ResolveTag(groupCtx, namespace, key, minimumTimestamp)
		if err == nil {
			foundA = true
			return nil
		} else if status.Code(err) == codes.NotFound {
			return nil
		}
		return util.StatusWrap(err, "Replica A")
	})
	group.Go(func() error {
		var err error
		signedValueB, completeB, err = r.replicaB.ResolveTag(groupCtx, namespace, key, minimumTimestamp)
		if err == nil {
			foundB = true
			return nil
		} else if status.Code(err) == codes.NotFound {
			return nil
		}
		return util.StatusWrap(err, "Replica B")
	})
	if err := group.Wait(); err != nil {
		var badSignedValue tag.SignedValue
		return badSignedValue, false, err
	}

	// Combine results from both replicas.
	//
	// If the tag is present in only a single replica, we return it,
	// but announce it as being incomplete. This should cause lease
	// renewing to replicate the tag to the other replica. We do the
	// same thing if the tag is present in both replicas, but having
	// a different value. There we prefer returning the newest value.
	if foundA && foundB {
		if !signedValueA.Equal(signedValueB) {
			tA, tB := signedValueA.Value.Timestamp, signedValueB.Value.Timestamp
			if tA.After(tB) {
				return signedValueA, false, nil
			}
			return signedValueB, false, nil
		}
		return signedValueA, completeA && completeB, nil
	} else if foundA {
		return signedValueA, false, nil
	} else if foundB {
		return signedValueB, false, nil
	}
	var badSignedValue tag.SignedValue
	return badSignedValue, false, status.Error(codes.NotFound, "Tag not found")
}
