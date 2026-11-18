package executewithstorage

import (
	"context"
	"crypto/ecdh"
	"iter"

	model_core "bonanza.build/pkg/model/core"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_encoding_pb "bonanza.build/pkg/proto/model/encoding"
	model_executewithstorage_pb "bonanza.build/pkg/proto/model/executewithstorage"
	"bonanza.build/pkg/remoteexecution"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
)

// Action that a client wants to run on a worker. The actual definition
// of the action is stored in an object.
//
// In addition to the reference, this struct contains the encoders that
// the client used to create the object, allowing the worker to decode
// it. The format needs to be provided so that the worker can reject
// misrouted requests.
//
// The reference may be nil, indicating that the action message is
// empty. As objects cannot be empty, no object is stored in that case.
type Action[TReference any] struct {
	Reference *model_core.Decodable[TReference]
	Encoders  []*model_encoding_pb.BinaryEncoder
	Format    *model_core_pb.ObjectFormat
}

// NamespacedAction is an Action whose object, and any objects it
// references, are stored in a given namespace. Any execution events and
// results are written to the same namespace.
type NamespacedAction struct {
	Namespace object.Namespace
	Action[object.LocalReference]
}

type protoClient struct {
	base remoteexecution.Client[*model_executewithstorage_pb.Action, *model_executewithstorage_pb.Event, *model_executewithstorage_pb.Result]
}

// NewProtoClient creates a decorator for the remote execution client
// that makes it simpler to run actions on workers that make use of the
// "executewithstorage" protocol (i.e., workers that rely on actions,
// execution events, and results to be backed by object storage).
//
// The advantage of using this decorator is that the caller can use
// native types for decodable references, as opposed to sending and
// receiving their Protobuf message equivalents. If the worker does not
// provide execution event or result references, these are set to nil,
// indicating that these messages are empty.
func NewProtoClient(
	base remoteexecution.Client[*model_executewithstorage_pb.Action, *model_executewithstorage_pb.Event, *model_executewithstorage_pb.Result],
) remoteexecution.Client[*NamespacedAction, *model_core.Decodable[object.LocalReference], *model_core.Decodable[object.LocalReference]] {
	return &protoClient{
		base: base,
	}
}

func (c *protoClient) RunAction(
	ctx context.Context,
	platformECDHPublicKey *ecdh.PublicKey,
	action *NamespacedAction,
	actionAdditionalData *encryptedaction_pb.Action_AdditionalData,
	resultReferenceOut **model_core.Decodable[object.LocalReference],
	errOut *error,
) iter.Seq[*model_core.Decodable[object.LocalReference]] {
	// Convert the action reference.
	var resultMessage *model_executewithstorage_pb.Result
	var errBase error
	executionEventMessages := c.base.RunAction(
		ctx,
		platformECDHPublicKey,
		&model_executewithstorage_pb.Action{
			Namespace:       action.Namespace.ToProto(),
			ActionEncoders:  action.Encoders,
			ActionReference: model_core.OptionalDecodableLocalReferenceToWeakProto(action.Reference),
			ActionFormat:    action.Format,
		},
		actionAdditionalData,
		&resultMessage,
		&errBase,
	)

	referenceFormat := action.Namespace.ReferenceFormat
	return func(yield func(*model_core.Decodable[object.LocalReference]) bool) {
		// Convert the execution event references.
		for executionEventMessage := range executionEventMessages {
			executionEventReference, err := model_core.NewOptionalDecodableLocalReferenceFromWeakProto(referenceFormat, executionEventMessage.EventReference)
			if err != nil {
				*errOut = util.StatusWrapWithCode(err, codes.Internal, "Received invalid execution event reference")
				return
			}
			if !yield(executionEventReference) {
				break
			}
		}

		if errBase != nil {
			*errOut = errBase
			return
		}

		// Convert the result reference.
		resultReference, err := model_core.NewOptionalDecodableLocalReferenceFromWeakProto(referenceFormat, resultMessage.GetResultReference())
		if err != nil {
			*errOut = util.StatusWrapWithCode(err, codes.Internal, "Received invalid result reference")
			return
		}
		*resultReferenceOut = resultReference
		*errOut = nil
	}
}
