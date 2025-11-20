package analysis

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"bonanza.build/pkg/crypto"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

var fetchActionObjectFormat = model_core.NewProtoObjectFormat(&model_fetch_pb.Action{})

func (c *baseComputer[TReference, TMetadata]) ComputeHttpFileContentsValue(ctx context.Context, key *model_analysis_pb.HttpFileContents_Key, e HttpFileContentsEnvironment[TReference, TMetadata]) (PatchedHttpFileContentsValue[TMetadata], error) {
	actionEncodersValue := e.GetActionEncodersValue(&model_analysis_pb.ActionEncoders_Key{})
	actionEncoder, gotActionEncoder := e.GetActionEncoderObjectValue(&model_analysis_pb.ActionEncoderObject_Key{})
	actionReaders, gotActionReaders := e.GetActionReadersValue(&model_analysis_pb.ActionReaders_Key{})
	fetchPlatform := e.GetRegisteredFetchPlatformValue(&model_analysis_pb.RegisteredFetchPlatform_Key{})
	fileCreationParametersValue := e.GetFileCreationParametersValue(&model_analysis_pb.FileCreationParameters_Key{})
	registeredFetchPlatformValue := e.GetRegisteredFetchPlatformValue(&model_analysis_pb.RegisteredFetchPlatform_Key{})
	if !actionEncodersValue.IsSet() ||
		!gotActionEncoder ||
		!gotActionReaders ||
		!fetchPlatform.IsSet() ||
		!fileCreationParametersValue.IsSet() ||
		!registeredFetchPlatformValue.IsSet() {
		return PatchedHttpFileContentsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	fetchPlatformECDHPublicKey, err := crypto.ParsePKIXECDHPublicKey(registeredFetchPlatformValue.Message.FetchPlatformPkixPublicKey)
	if err != nil {
		return PatchedHttpFileContentsValue[TMetadata]{}, fmt.Errorf("invalid fetch platform PKIX public key: %w", err)
	}

	fetchOptions := key.FetchOptions
	if fetchOptions == nil {
		return PatchedHttpFileContentsValue[TMetadata]{}, errors.New("no fetch options provided")
	}

	target := fetchOptions.Target
	if target == nil {
		return PatchedHttpFileContentsValue[TMetadata]{}, errors.New("no target provided")
	}

	referenceFormat := c.referenceFormat
	createdAction, err := model_core.MarshalAndEncodeDeterministic(
		model_core.NewSimplePatchedMessage[TMetadata](
			model_core.NewProtoBinaryMarshaler(&model_fetch_pb.Action{
				FileCreationParameters: fileCreationParametersValue.Message.FileCreationParameters,
				Target:                 target,
			}),
		),
		referenceFormat,
		actionEncoder,
	)
	capturedAction, err := createdAction.Value.Capture(ctx, e)
	if err != nil {
		return PatchedHttpFileContentsValue[TMetadata]{}, err
	}

	// Compute a stable fingerprint for this action. Assume that
	// only the URLs in the target to be fetched contribute to the
	// amount of time it takes to fulfil the request.
	var stableTarget anypb.Any
	marshalOptions := proto.MarshalOptions{Deterministic: true}
	if err := anypb.MarshalFrom(
		&stableTarget,
		&model_fetch_pb.Target{Urls: target.Urls},
		marshalOptions,
	); err != nil {
		return PatchedHttpFileContentsValue[TMetadata]{}, fmt.Errorf("failed to marshal stable target: %w", err)
	}
	marshaledStableTarget, err := marshalOptions.Marshal(&stableTarget)
	if err != nil {
		return PatchedHttpFileContentsValue[TMetadata]{}, fmt.Errorf("failed to marshal stable target: %w", err)
	}
	stableFingerprint := sha256.Sum256(marshaledStableTarget)

	var resultReference model_core.Decodable[TReference]
	var errExecution error
	for range c.executionClient.RunAction(
		ctx,
		fetchPlatformECDHPublicKey,
		&model_executewithstorage.Action[TReference]{
			Reference: model_core.CopyDecodable(
				createdAction,
				e.ReferenceObject(capturedAction),
			),
			Encoders: actionEncodersValue.Message.ActionEncoders,
			Format:   fetchActionObjectFormat,
		},
		&encryptedaction_pb.Action_AdditionalData{
			StableFingerprint: stableFingerprint[:],
			ExecutionTimeout:  &durationpb.Duration{Seconds: 3600},
		},
		&resultReference,
		&errExecution,
	) {
		// TODO: Capture and propagate execution events?
	}
	if errExecution != nil {
		return PatchedHttpFileContentsValue[TMetadata]{}, errExecution
	}

	result, err := actionReaders.FetchResult.ReadParsedObject(ctx, resultReference)
	if err != nil {
		return PatchedHttpFileContentsValue[TMetadata]{}, fmt.Errorf("failed to read completion event: %w", err)
	}

	switch outcome := result.Message.Outcome.(type) {
	case *model_fetch_pb.Result_Success_:
		success := model_core.Patch(e, model_core.Nested(result, outcome.Success))
		return model_core.NewPatchedMessage(
			&model_analysis_pb.HttpFileContents_Value{
				Exists: success.Message,
			},
			success.Patcher,
		), nil
	case *model_fetch_pb.Result_Failure:
		err := status.ErrorProto(outcome.Failure)
		if fetchOptions.AllowFail || status.Code(err) == codes.NotFound {
			return model_core.NewSimplePatchedMessage[TMetadata](
				&model_analysis_pb.HttpFileContents_Value{},
			), nil
		}
		return PatchedHttpFileContentsValue[TMetadata]{}, fmt.Errorf("failed to fetch file: %w", err)
	default:
		return PatchedHttpFileContentsValue[TMetadata]{}, errors.New("unkown fetch result type")
	}
}
