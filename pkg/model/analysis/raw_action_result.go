package analysis

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"

	"bonanza.build/pkg/crypto"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_encoding "bonanza.build/pkg/model/encoding"
	"bonanza.build/pkg/model/evaluation"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	"bonanza.build/pkg/storage/object"
)

var commandActionObjectFormat = model_core.NewProtoObjectFormat(&model_command_pb.Action{})

func (c *baseComputer[TReference, TMetadata]) ComputeRawActionResultValue(ctx context.Context, key model_core.Message[*model_analysis_pb.RawActionResult_Key, TReference], e RawActionResultEnvironment[TReference, TMetadata]) (PatchedRawActionResultValue[TMetadata], error) {
	actionEncodersValue := e.GetActionEncodersValue(&model_analysis_pb.ActionEncoders_Key{})
	actionReaders, gotActionReaders := e.GetActionReadersValue(&model_analysis_pb.ActionReaders_Key{})
	if !actionEncodersValue.IsSet() || !gotActionReaders {
		return PatchedRawActionResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Obtain the public key of the target platform, which is used
	// to route the request to the right worker and to encrypt the
	// action.
	executeRequest := model_core.Nested(key, key.Message.ExecuteRequest)
	if executeRequest.Message == nil {
		return PatchedRawActionResultValue[TMetadata]{}, errors.New("no execute request specified")
	}
	platformECDHPublicKey, err := crypto.ParsePKIXECDHPublicKey(executeRequest.Message.PlatformPkixPublicKey)
	if err != nil {
		return PatchedRawActionResultValue[TMetadata]{}, fmt.Errorf("invalid platform PKIX public key: %w", err)
	}

	// Use the reference of the Command message as the stable
	// fingerprint of the action, which the scheduler can use to
	// keep track of performance characteristics. Compute a hash to
	// masquerade the actual Command reference.
	actionReference, err := model_core.FlattenDecodableReference(model_core.Nested(executeRequest, executeRequest.Message.ActionReference))
	if err != nil {
		return PatchedRawActionResultValue[TMetadata]{}, fmt.Errorf("invalid action reference: %w", err)
	}
	action, err := actionReaders.CommandAction.ReadObject(ctx, actionReference)
	if err != nil {
		return PatchedRawActionResultValue[TMetadata]{}, fmt.Errorf("failed to read action: %w", err)
	}
	commandReference, err := model_core.FlattenDecodableReference(model_core.Nested(action, action.Message.CommandReference))
	if err != nil {
		return PatchedRawActionResultValue[TMetadata]{}, fmt.Errorf("invalid command reference: %w", err)
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
			Format:    commandActionObjectFormat,
		},
		&encryptedaction_pb.Action_AdditionalData{
			StableFingerprint: commandReferenceSHA256[:],
			ExecutionTimeout:  executeRequest.Message.ExecutionTimeout,
		},
		&resultReference,
		&errExecution,
	) {
		// TODO: Capture and propagate execution events?
	}
	if errExecution != nil {
		return PatchedRawActionResultValue[TMetadata]{}, errExecution
	}

	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.RawActionResult_Value {
		return &model_analysis_pb.RawActionResult_Value{
			ResultReference: patcher.AddDecodableReference(
				model_core.CopyDecodable(
					resultReference,
					model_core.CaptureExistingObject(e, resultReference.Value),
				),
			),
		}
	}), nil
}

func convertDictToEnvironmentVariableList[TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	environment map[string]string,
	actionEncoder model_encoding.DeterministicBinaryEncoder,
	referenceFormat object.ReferenceFormat,
	capturer model_core.CreatedObjectCapturer[TMetadata],
) (model_core.PatchedMessage[[]*model_command_pb.EnvironmentVariableList_Element, TMetadata], btree.ParentNodeComputer[*model_command_pb.EnvironmentVariableList_Element, TMetadata], error) {
	parentNodeComputer := btree.Capturing(ctx, capturer, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_command_pb.EnvironmentVariableList_Element, object.LocalReference]) model_core.PatchedMessage[*model_command_pb.EnvironmentVariableList_Element, TMetadata] {
		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_command_pb.EnvironmentVariableList_Element {
			return &model_command_pb.EnvironmentVariableList_Element{
				Level: &model_command_pb.EnvironmentVariableList_Element_Parent{
					Parent: patcher.AddDecodableReference(createdObject),
				},
			}
		})
	})
	environmentVariablesBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			/* minimumSizeBytes = */ 1<<16,
			/* maximumSizeBytes = */ 1<<18,
			/* isParent = */ func(element *model_command_pb.EnvironmentVariableList_Element) bool {
				return element.GetParent() != nil
			},
		),
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
