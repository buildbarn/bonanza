package executewithstorage

import (
	"context"
	"crypto/ecdh"
	"iter"

	model_core "bonanza.build/pkg/model/core"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	"bonanza.build/pkg/remoteexecution"
	"bonanza.build/pkg/storage/object"
)

type namespaceAddingClient struct {
	base      remoteexecution.Client[*NamespacedAction, *model_core.Decodable[object.LocalReference], *model_core.Decodable[object.LocalReference]]
	namespace object.Namespace
}

// NewNamespaceAddingClient creates a decorator for a remote execution
// client that can add a namespace (i.e., instance name and reference
// format) to any outgoing execution requests. This decorator can be
// used to abstract away the notion of instance names in contexts where
// local object references are sufficient.
func NewNamespaceAddingClient(
	base remoteexecution.Client[*NamespacedAction, *model_core.Decodable[object.LocalReference], *model_core.Decodable[object.LocalReference]],
	namespace object.Namespace,
) remoteexecution.Client[*Action[object.LocalReference], *model_core.Decodable[object.LocalReference], *model_core.Decodable[object.LocalReference]] {
	return &namespaceAddingClient{
		base:      base,
		namespace: namespace,
	}
}

func (c *namespaceAddingClient) RunAction(
	ctx context.Context,
	platformECDHPublicKey *ecdh.PublicKey,
	action *Action[object.LocalReference],
	actionAdditionalData *encryptedaction_pb.Action_AdditionalData,
	resultReferenceOut **model_core.Decodable[object.LocalReference],
	errOut *error,
) iter.Seq[*model_core.Decodable[object.LocalReference]] {
	return c.base.RunAction(
		ctx,
		platformECDHPublicKey,
		&NamespacedAction{
			Namespace: c.namespace,
			Action:    *action,
		},
		actionAdditionalData,
		resultReferenceOut,
		errOut,
	)
}
