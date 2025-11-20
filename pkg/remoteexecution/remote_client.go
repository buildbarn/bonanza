package remoteexecution

import (
	"context"
	"crypto/ecdh"
	"crypto/x509"
	"iter"

	"bonanza.build/pkg/encryptedaction"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	remoteexecution_pb "bonanza.build/pkg/proto/remoteexecution"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type remoteClient struct {
	executionClient        remoteexecution_pb.ExecutionClient
	clientPrivateKey       *ecdh.PrivateKey
	clientCertificateChain [][]byte
}

// NewRemoteClient creates a new remote execution client that uses a
// provided gRPC client and X.509 client key pair.
func NewRemoteClient(
	executionClient remoteexecution_pb.ExecutionClient,
	clientPrivateKey *ecdh.PrivateKey,
	clientCertificateChain [][]byte,
) Client[[]byte, []byte, []byte] {
	return &remoteClient{
		executionClient:        executionClient,
		clientPrivateKey:       clientPrivateKey,
		clientCertificateChain: clientCertificateChain,
	}
}

// RunAction encrypts an action and sends it to a scheduler to request
// its execution on a worker. An iterator is returned that yields any
// execution events reported by the worker. Upon completion, the result
// is set.
func (c *remoteClient) RunAction(ctx context.Context, platformECDHPublicKey *ecdh.PublicKey, actionPlaintext []byte, actionAdditionalData *encryptedaction_pb.Action_AdditionalData, result *[]byte, errOut *error) iter.Seq[[]byte] {
	marshaledPlatformECDHPublicKey, err := x509.MarshalPKIXPublicKey(platformECDHPublicKey)
	if err != nil {
		*errOut = util.StatusWrapfWithCode(err, codes.InvalidArgument, "Failed to obtain marshal platform ECDH public key")
		return func(func([]byte) bool) {}
	}

	// Compute shared secret for encrypting the action.
	sharedSecret, err := c.clientPrivateKey.ECDH(platformECDHPublicKey)
	if err != nil {
		*errOut = util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to obtain shared secret")
		return func(func([]byte) bool) {}
	}

	// Encrypt the action.
	action := &encryptedaction_pb.Action{
		PlatformPkixPublicKey:  marshaledPlatformECDHPublicKey,
		ClientCertificateChain: c.clientCertificateChain,
		AdditionalData:         actionAdditionalData,
	}
	if err := encryptedaction.ActionSetCiphertext(action, sharedSecret, actionPlaintext); err != nil {
		*errOut = err
		return func(func([]byte) bool) {}
	}

	return func(yield func([]byte) bool) {
		ctxWithCancel, cancel := context.WithCancel(ctx)
		defer cancel()
		client, err := c.executionClient.Execute(ctxWithCancel, &remoteexecution_pb.ExecuteRequest{
			Action: action,
			// TODO: Priority.
		})
		if err != nil {
			*errOut = err
			return
		}
		defer func() {
			cancel()
			for {
				if _, err := client.Recv(); err != nil {
					return
				}
			}
		}()

		eventEncoder := encryptedaction.NewEventEncoder(action, sharedSecret)
		for {
			response, err := client.Recv()
			if err != nil {
				*errOut = err
				return
			}

			switch stage := response.Stage.(type) {
			case *remoteexecution_pb.ExecuteResponse_Executing_:
				// Worker has posted an execution event.
				// Unmarshal it and yield it to the
				// caller.
				if lastEventMessage := stage.Executing.LastEvent; lastEventMessage != nil {
					lastEvent, err := eventEncoder.DecodeEvent(lastEventMessage, false)
					if err != nil {
						*errOut = util.StatusWrapWithCode(err, codes.Internal, "Failed to decrypt last event")
						return
					}

					if !yield(lastEvent) {
						*errOut = status.Error(codes.Canceled, "Caller canceled iteration of execution events")
						return
					}
				}
			case *remoteexecution_pb.ExecuteResponse_Completed_:
				// Action has completed. Unmarshal and
				// return the completion event.
				completionEventMessage := stage.Completed.CompletionEvent
				if completionEventMessage == nil {
					*errOut = status.Error(codes.Internal, "Action completed, but no completion event was returned")
					return
				}

				completionEvent, err := eventEncoder.DecodeEvent(completionEventMessage, true)
				if err != nil {
					*errOut = util.StatusWrapWithCode(err, codes.Internal, "Failed to decrypt completion event")
					return
				}
				*result = completionEvent
				*errOut = nil
				return
			}
		}
	}
}
