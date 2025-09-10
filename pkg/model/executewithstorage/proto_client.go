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
type Action[TReference any] struct {
	Reference model_core.Decodable[TReference]
	Encoders  []*model_encoding_pb.BinaryEncoder
	Format    *model_core_pb.ObjectFormat
}

type protoClient struct {
	base remoteexecution.Client[*model_executewithstorage_pb.Action, *model_core_pb.WeakDecodableReference, *model_core_pb.WeakDecodableReference]
}

// NewProtoClient creates a decorator for the remote execution client
// that makes it simpler to run actions on workers that make use of the
// "executewithstorage" protocol (i.e., workers that rely on actions,
// execution events, and results to be backed by object storage).
//
// The advantage of using this decorator is that the caller can use
// native types for decodable references, as opposed to sending and
// receiving their Protobuf message equivalents.
func NewProtoClient(
	base remoteexecution.Client[*model_executewithstorage_pb.Action, *model_core_pb.WeakDecodableReference, *model_core_pb.WeakDecodableReference],
) remoteexecution.Client[*Action[object.GlobalReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]] {
	return &protoClient{
		base: base,
	}
}

func (c *protoClient) RunAction(
	ctx context.Context,
	platformECDHPublicKey *ecdh.PublicKey,
	action *Action[object.GlobalReference],
	actionAdditionalData *encryptedaction_pb.Action_AdditionalData,
	resultReferenceOut *model_core.Decodable[object.LocalReference],
	errOut *error,
) iter.Seq[model_core.Decodable[object.LocalReference]] {
	// Convert the action reference.
	var resultReferenceMessage *model_core_pb.WeakDecodableReference
	var errBase error
	namespace := action.Reference.Value.GetNamespace()
	executionEventMessages := c.base.RunAction(
		ctx,
		platformECDHPublicKey,
		&model_executewithstorage_pb.Action{
			Namespace:       namespace.ToProto(),
			ActionEncoders:  action.Encoders,
			ActionReference: model_core.DecodableLocalReferenceToWeakProto(action.Reference),
			ActionFormat:    action.Format,
		},
		actionAdditionalData,
		&resultReferenceMessage,
		&errBase,
	)

	referenceFormat := namespace.ReferenceFormat
	return func(yield func(model_core.Decodable[object.LocalReference]) bool) {
		// Convert the execution event references.
		for executionEventMessage := range executionEventMessages {
			executionEventReference, err := model_core.NewDecodableLocalReferenceFromWeakProto(referenceFormat, executionEventMessage)
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
		resultReference, err := model_core.NewDecodableLocalReferenceFromWeakProto(referenceFormat, resultReferenceMessage)
		if err != nil {
			*errOut = util.StatusWrapWithCode(err, codes.Internal, "Received invalid result reference")
			return
		}
		*resultReferenceOut = resultReference
		*errOut = nil
	}
}
