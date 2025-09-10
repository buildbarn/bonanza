package executewithstorage

import (
	"context"
	"crypto/ecdh"
	"iter"

	model_core "bonanza.build/pkg/model/core"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	"bonanza.build/pkg/remoteexecution"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/object/namespacemapping"
)

type namespaceAddingClient[TNamespace namespacemapping.NamespaceAddingNamespace[TReference], TReference any] struct {
	base      remoteexecution.Client[*Action[TReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]]
	namespace TNamespace
}

// NewNamespaceAddingClient creates a decorator for a remote execution
// client that can add a namespace (e.g., instance name) to any outgoing
// execution requests. This decorator can be used to abstract away the
// notion of instance names in contexts where local object references
// are sufficient.
func NewNamespaceAddingClient[TNamespace namespacemapping.NamespaceAddingNamespace[TReference], TReference any](
	base remoteexecution.Client[*Action[TReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]],
	namespace TNamespace,
) remoteexecution.Client[*Action[object.LocalReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]] {
	return &namespaceAddingClient[TNamespace, TReference]{
		base:      base,
		namespace: namespace,
	}
}

func (c *namespaceAddingClient[TNamespace, TReference]) RunAction(
	ctx context.Context,
	platformECDHPublicKey *ecdh.PublicKey,
	action *Action[object.LocalReference],
	actionAdditionalData *encryptedaction_pb.Action_AdditionalData,
	resultReferenceOut *model_core.Decodable[object.LocalReference],
	errOut *error,
) iter.Seq[model_core.Decodable[object.LocalReference]] {
	return c.base.RunAction(
		ctx,
		platformECDHPublicKey,
		&Action[TReference]{
			Reference: model_core.CopyDecodable(
				action.Reference,
				c.namespace.WithLocalReference(
					action.Reference.Value,
				),
			),
			Encoders: action.Encoders,
			Format:   action.Format,
		},
		actionAdditionalData,
		resultReferenceOut,
		errOut,
	)
}
