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
	model_format "bonanza.build/pkg/model/format"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

var fetchActionObjectFormat = model_format.NewProto[model_fetch_pb.Action]().ToProto()

func (c *baseComputer[TReference, TMetadata]) ComputeHTTPFileContentsValue(ctx context.Context, key *model_analysis_pb.HttpFileContents_Key, e HTTPFileContentsEnvironment[TReference, TMetadata]) (PatchedHTTPFileContentsValue[TMetadata], error) {
	actionEncodersValue := e.GetActionEncodersValue(&model_analysis_pb.ActionEncoders_Key{})
	actionEncoders, gotActionEncoders := e.GetActionEncodersObject(&model_analysis_pb.ActionEncoders_Key{})
	fetchPlatform := e.GetRegisteredFetchPlatformValue(&model_analysis_pb.RegisteredFetchPlatform_Key{})
	fileCreationParametersValue := e.GetFileCreationParametersValue(&model_analysis_pb.FileCreationParameters_Key{})
	registeredFetchPlatformValue := e.GetRegisteredFetchPlatformValue(&model_analysis_pb.RegisteredFetchPlatform_Key{})
	if !actionEncodersValue.IsSet() ||
		!gotActionEncoders ||
		!fetchPlatform.IsSet() ||
		!fileCreationParametersValue.IsSet() ||
		!registeredFetchPlatformValue.IsSet() {
		return PatchedHTTPFileContentsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	fetchPlatformECDHPublicKey, err := crypto.ParsePKIXECDHPublicKey(registeredFetchPlatformValue.Message.GetFetchPlatformPkixPublicKey())
	if err != nil {
		return PatchedHTTPFileContentsValue[TMetadata]{}, fmt.Errorf("invalid fetch platform PKIX public key: %w", err)
	}

	fetchOptions := key.GetFetchOptions()
	if fetchOptions == nil {
		return PatchedHTTPFileContentsValue[TMetadata]{}, errors.New("no fetch options provided")
	}

	target := fetchOptions.GetTarget()
	if target == nil {
		return PatchedHTTPFileContentsValue[TMetadata]{}, errors.New("no target provided")
	}

	referenceFormat := c.referenceFormat
	createdAction, err := model_core.MarshalAndEncodeDeterministic(
		model_core.NewSimplePatchedMessage[TMetadata](
			model_format.NewProto[model_fetch_pb.Action]().NewMarshaler(&model_fetch_pb.Action{
				FileCreationParameters: fileCreationParametersValue.Message.GetFileCreationParameters(),
				Target:                 target,
			}),
		),
		referenceFormat,
		actionEncoders.Encoder,
	)
	capturedAction, err := createdAction.Value.Capture(ctx, e)
	if err != nil {
		return PatchedHTTPFileContentsValue[TMetadata]{}, err
	}

	// Compute a stable fingerprint for this action. Assume that
	// only the URLs in the target to be fetched contribute to the
	// amount of time it takes to fulfil the request.
	var stableTarget anypb.Any
	marshalOptions := proto.MarshalOptions{Deterministic: true}
	if err := anypb.MarshalFrom(
		&stableTarget, &model_fetch_pb.Target{
			Urls: target.GetUrls(),
		},
		marshalOptions,
	); err != nil {
		return PatchedHTTPFileContentsValue[TMetadata]{}, fmt.Errorf("failed to marshal stable target: %w", err)
	}
	marshaledStableTarget, err := marshalOptions.Marshal(&stableTarget)
	if err != nil {
		return PatchedHTTPFileContentsValue[TMetadata]{}, fmt.Errorf("failed to marshal stable target: %w", err)
	}
	stableFingerprint := sha256.Sum256(marshaledStableTarget)

	actionReference := model_core.CopyDecodable(
		createdAction,
		e.ReferenceObject(capturedAction),
	)
	var resultReference *model_core.Decodable[TReference]
	var errExecution error
	for update := range c.executionClient.RunAction(
		ctx,
		fetchPlatformECDHPublicKey,
		&model_executewithstorage.Action[TReference]{
			Reference: &actionReference,
			Encoders:  actionEncodersValue.Message.GetActionEncoders(),
			Format:    fetchActionObjectFormat,
		},
		encryptedaction_pb.Action_AdditionalData_builder{
			StableFingerprint: stableFingerprint[:],
			ExecutionTimeout:  &durationpb.Duration{Seconds: 3600},
		}.Build(),
		&resultReference,
		&errExecution,
	) {
		// Report the number of bytes downloaded by the fetcher.
		// The fetcher does not provide an event reference if
		// the event message is empty.
		if !update.HasEvent {
			continue
		}
		event := &model_fetch_pb.Event{}
		if update.Event != nil {
			m, err := actionEncoders.Readers.FetchEvent.ReadObject(ctx, *update.Event)
			if err != nil {
				return PatchedHTTPFileContentsValue[TMetadata]{}, fmt.Errorf("failed to read execution event: %w", err)
			}
			event = m.Message
		}
		e.ReportHTTPFileContentsProgress(&model_analysis_pb.HttpFileContents_Progress{
			Downloaded: &model_evaluation_pb.ProgressCounter{
				Current: event.GetDownloadedSizeBytes(),
				Total:   event.GetTotalSizeBytes(),
				Unit:    model_evaluation_pb.ProgressCounter_BYTES,
			},
		})
	}
	if errExecution != nil {
		return PatchedHTTPFileContentsValue[TMetadata]{}, errExecution
	}
	if resultReference == nil {
		return PatchedHTTPFileContentsValue[TMetadata]{}, errors.New("fetcher did not return a result")
	}

	result, err := actionEncoders.Readers.FetchResult.ReadObject(ctx, *resultReference)
	if err != nil {
		return PatchedHTTPFileContentsValue[TMetadata]{}, fmt.Errorf("failed to read completion event: %w", err)
	}

	switch result.Message.GetOutcome().(type) {
	case *model_fetch_pb.Result_Success_:
		success := model_core.Patch(e, model_core.Nested(result, result.Message.GetSuccess()))
		return model_core.NewPatchedMessage(
			&model_analysis_pb.HttpFileContents_Value{
				Exists: success.Message,
			},
			success.Patcher,
		), nil
	case *model_fetch_pb.Result_Failure:
		err := status.ErrorProto(result.Message.GetFailure())
		if fetchOptions.GetAllowFail() || status.Code(err) == codes.NotFound {
			return model_core.NewSimplePatchedMessage[TMetadata](
				&model_analysis_pb.HttpFileContents_Value{},
			), nil
		}
		return PatchedHTTPFileContentsValue[TMetadata]{}, fmt.Errorf("failed to fetch file: %w", err)
	default:
		return PatchedHTTPFileContentsValue[TMetadata]{}, errors.New("unkown fetch result type")
	}
}
