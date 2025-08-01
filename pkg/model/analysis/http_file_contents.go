package analysis

import (
	"context"
	"errors"
	"fmt"

	"bonanza.build/pkg/crypto"
	"bonanza.build/pkg/evaluation"
	model_core "bonanza.build/pkg/model/core"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"
	remoteexecution_pb "bonanza.build/pkg/proto/remoteexecution"
	"bonanza.build/pkg/storage/dag"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func (c *baseComputer[TReference, TMetadata]) ComputeHttpFileContentsValue(ctx context.Context, key *model_analysis_pb.HttpFileContents_Key, e HttpFileContentsEnvironment[TReference, TMetadata]) (PatchedHttpFileContentsValue, error) {
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
		return PatchedHttpFileContentsValue{}, evaluation.ErrMissingDependency
	}

	fetchPlatformECDHPublicKey, err := crypto.ParsePKIXECDHPublicKey(registeredFetchPlatformValue.Message.FetchPlatformPkixPublicKey)
	if err != nil {
		return PatchedHttpFileContentsValue{}, fmt.Errorf("invalid fetch platform PKIX public key: %w", err)
	}

	fetchOptions := key.FetchOptions
	if fetchOptions == nil {
		return PatchedHttpFileContentsValue{}, errors.New("no fetch options provided")
	}

	referenceFormat := c.getReferenceFormat()
	createdAction, err := model_core.MarshalAndEncode(
		model_core.NewSimplePatchedMessage[TMetadata](
			model_core.NewProtoMarshalable(&model_fetch_pb.Action{
				FileCreationParameters: fileCreationParametersValue.Message.FileCreationParameters,
				Target:                 fetchOptions.Target,
			}),
		),
		referenceFormat,
		actionEncoder,
	)

	var resultReference model_core.Decodable[TReference]
	var errExecution error
	for range c.executionClient.RunAction(
		ctx,
		fetchPlatformECDHPublicKey,
		&model_executewithstorage.Action[TReference]{
			Reference: model_core.CopyDecodable(
				createdAction,
				e.ReferenceObject(
					createdAction.Value.GetLocalReference(),
					e.CaptureCreatedObject(createdAction.Value),
				),
			),
			Encoders: actionEncodersValue.Message.ActionEncoders,
			Format: &model_core_pb.ObjectFormat{
				Format: &model_core_pb.ObjectFormat_ProtoTypeName{
					ProtoTypeName: "bonanza.model.fetch.Action",
				},
			},
		},
		&remoteexecution_pb.Action_AdditionalData{
			ExecutionTimeout: &durationpb.Duration{Seconds: 3600},
		},
		&resultReference,
		&errExecution,
	) {
		// TODO: Capture and propagate execution events?
	}
	if errExecution != nil {
		return PatchedHttpFileContentsValue{}, errExecution
	}

	result, err := actionReaders.FetchResult.ReadParsedObject(ctx, resultReference)
	if err != nil {
		return PatchedHttpFileContentsValue{}, fmt.Errorf("failed to read completion event: %w", err)
	}

	switch outcome := result.Message.Outcome.(type) {
	case *model_fetch_pb.Result_Success_:
		success := model_core.Patch(e, model_core.Nested(result, outcome.Success))
		return model_core.NewPatchedMessage(
			&model_analysis_pb.HttpFileContents_Value{
				Exists: success.Message,
			},
			model_core.MapReferenceMetadataToWalkers(success.Patcher),
		), nil
	case *model_fetch_pb.Result_Failure:
		err := status.ErrorProto(outcome.Failure)
		if fetchOptions.AllowFail || status.Code(err) == codes.NotFound {
			return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](
				&model_analysis_pb.HttpFileContents_Value{},
			), nil
		}
		return PatchedHttpFileContentsValue{}, fmt.Errorf("failed to fetch file: %w", err)
	default:
		return PatchedHttpFileContentsValue{}, errors.New("unkown fetch result type")
	}
}
