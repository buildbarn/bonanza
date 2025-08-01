package analysis

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"

	"bonanza.build/pkg/crypto"
	"bonanza.build/pkg/evaluation"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	remoteexecution_pb "bonanza.build/pkg/proto/remoteexecution"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/status"
)

func (c *baseComputer[TReference, TMetadata]) ComputeActionResultValue(ctx context.Context, key model_core.Message[*model_analysis_pb.ActionResult_Key, TReference], e ActionResultEnvironment[TReference, TMetadata]) (PatchedActionResultValue, error) {
	actionEncodersValue := e.GetActionEncodersValue(&model_analysis_pb.ActionEncoders_Key{})
	actionReaders, gotActionReaders := e.GetActionReadersValue(&model_analysis_pb.ActionReaders_Key{})
	if !actionEncodersValue.IsSet() || !gotActionReaders {
		return PatchedActionResultValue{}, evaluation.ErrMissingDependency
	}

	// Obtain the public key of the target platform, which is used
	// to route the request to the right worker and to encrypt the
	// action.
	executeRequest := model_core.Nested(key, key.Message.ExecuteRequest)
	if executeRequest.Message == nil {
		return PatchedActionResultValue{}, errors.New("no execute request specified")
	}
	platformECDHPublicKey, err := crypto.ParsePKIXECDHPublicKey(executeRequest.Message.PlatformPkixPublicKey)
	if err != nil {
		return PatchedActionResultValue{}, fmt.Errorf("invalid platform PKIX public key: %w", err)
	}

	// Use the reference of the Command message as the stable
	// fingerprint of the action, which the scheduler can use to
	// keep track of performance characteristics. Compute a hash to
	// masquerade the actual Command reference.
	actionReference, err := model_core.FlattenDecodableReference(model_core.Nested(executeRequest, executeRequest.Message.ActionReference))
	if err != nil {
		return PatchedActionResultValue{}, fmt.Errorf("invalid action reference: %w", err)
	}
	action, err := actionReaders.CommandAction.ReadParsedObject(ctx, actionReference)
	if err != nil {
		return PatchedActionResultValue{}, fmt.Errorf("failed to read action: %w", err)
	}
	commandReference, err := model_core.FlattenDecodableReference(model_core.Nested(action, action.Message.CommandReference))
	if err != nil {
		return PatchedActionResultValue{}, fmt.Errorf("invalid command reference: %w", err)
	}
	commandReferenceSHA256 := sha256.Sum256(commandReference.Value.GetRawReference())

	var resultReference model_core.Decodable[TReference]
	var errExecution error
	for range c.executionClient.RunAction(
		ctx,
		platformECDHPublicKey,
		&model_executewithstorage.Action[TReference]{
			Reference: actionReference,
			Encoders:  actionEncodersValue.Message.ActionEncoders,
			Format: &model_core_pb.ObjectFormat{
				Format: &model_core_pb.ObjectFormat_ProtoTypeName{
					ProtoTypeName: "bonanza.model.command.Action",
				},
			},
		},
		&remoteexecution_pb.Action_AdditionalData{
			StableFingerprint: commandReferenceSHA256[:],
			ExecutionTimeout:  executeRequest.Message.ExecutionTimeout,
		},
		&resultReference,
		&errExecution,
	) {
		// TODO: Capture and propagate execution events?
	}
	if errExecution != nil {
		return PatchedActionResultValue{}, errExecution
	}

	result, err := actionReaders.CommandResult.ReadParsedObject(ctx, resultReference)
	if err != nil {
		return PatchedActionResultValue{}, fmt.Errorf("failed to read completion event: %w", err)
	}
	if err := status.ErrorProto(result.Message.Status); err != nil {
		return PatchedActionResultValue{}, err
	}
	outputsReference := model_core.Patch(e, model_core.Nested(result, result.Message.OutputsReference))
	return model_core.NewPatchedMessage(
		&model_analysis_pb.ActionResult_Value{
			ExitCode:         result.Message.ExitCode,
			OutputsReference: outputsReference.Message,
		},
		model_core.MapReferenceMetadataToWalkers(outputsReference.Patcher),
	), nil
}

func convertDictToEnvironmentVariableList[TMetadata model_core.ReferenceMetadata](
	environment map[string]string,
	actionEncoder model_encoding.BinaryEncoder,
	referenceFormat object.ReferenceFormat,
	capturer model_core.CreatedObjectCapturer[TMetadata],
) (model_core.PatchedMessage[[]*model_command_pb.EnvironmentVariableList_Element, TMetadata], btree.ParentNodeComputer[*model_command_pb.EnvironmentVariableList_Element, TMetadata], error) {
	parentNodeComputer := func(createdObject model_core.Decodable[model_core.CreatedObject[TMetadata]], childNodes []*model_command_pb.EnvironmentVariableList_Element) model_core.PatchedMessage[*model_command_pb.EnvironmentVariableList_Element, TMetadata] {
		return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_command_pb.EnvironmentVariableList_Element {
			return &model_command_pb.EnvironmentVariableList_Element{
				Level: &model_command_pb.EnvironmentVariableList_Element_Parent{
					Parent: patcher.CaptureAndAddDecodableReference(createdObject, capturer),
				},
			}
		})
	}
	environmentVariablesBuilder := btree.NewSplitProllyBuilder(
		1<<16,
		1<<18,
		btree.NewObjectCreatingNodeMerger(
			actionEncoder,
			referenceFormat,
			parentNodeComputer,
		),
	)
	for _, name := range slices.Sorted(maps.Keys(environment)) {
		if err := environmentVariablesBuilder.PushChild(
			model_core.NewSimplePatchedMessage[TMetadata](&model_command_pb.EnvironmentVariableList_Element{
				Level: &model_command_pb.EnvironmentVariableList_Element_Leaf_{
					Leaf: &model_command_pb.EnvironmentVariableList_Element_Leaf{
						Name:  name,
						Value: environment[name],
					},
				},
			}),
		); err != nil {
			return model_core.PatchedMessage[[]*model_command_pb.EnvironmentVariableList_Element, TMetadata]{}, nil, err
		}
	}
	envList, err := environmentVariablesBuilder.FinalizeList()
	return envList, parentNodeComputer, err
}
