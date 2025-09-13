package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"time"

	"bonanza.build/pkg/crypto"
	model_analysis "bonanza.build/pkg/model/analysis"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/core/buffered"
	"bonanza.build/pkg/model/encoding"
	model_encoding "bonanza.build/pkg/model/encoding"
	"bonanza.build/pkg/model/evaluation"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	"bonanza.build/pkg/proto/configuration/bonanza_builder"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_build_pb "bonanza.build/pkg/proto/model/build"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	model_executewithstorage_pb "bonanza.build/pkg/proto/model/executewithstorage"
	remoteexecution_pb "bonanza.build/pkg/proto/remoteexecution"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	dag_pb "bonanza.build/pkg/proto/storage/dag"
	object_pb "bonanza.build/pkg/proto/storage/object"
	remoteexecution "bonanza.build/pkg/remoteexecution"
	"bonanza.build/pkg/remoteworker"
	"bonanza.build/pkg/storage/object"
	object_existenceprecondition "bonanza.build/pkg/storage/object/existenceprecondition"
	object_grpc "bonanza.build/pkg/storage/object/grpc"
	object_namespacemapping "bonanza.build/pkg/storage/object/namespacemapping"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/pool"
	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/global"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/buildbarn/bb-storage/pkg/x509"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"go.starlark.net/starlark"
)

func main() {
	program.RunMain(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		if len(os.Args) != 2 {
			return status.Error(codes.InvalidArgument, "Usage: bonanza_builder bonanza_builder.jsonnet")
		}
		var configuration bonanza_builder.ApplicationConfiguration
		if err := util.UnmarshalConfigurationFromFile(os.Args[1], &configuration); err != nil {
			return util.StatusWrapf(err, "Failed to read configuration from %s", os.Args[1])
		}
		lifecycleState, grpcClientFactory, err := global.ApplyConfiguration(configuration.Global, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to apply global configuration options")
		}

		storageGRPCClient, err := grpcClientFactory.NewClientFromConfiguration(configuration.StorageGrpcClient, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to create storage gRPC client")
		}
		objectDownloader := object_existenceprecondition.NewDownloader(
			object_grpc.NewGRPCDownloader(
				object_pb.NewDownloaderClient(storageGRPCClient),
			),
		)
		parsedObjectPool, err := model_parser.NewParsedObjectPoolFromConfiguration(configuration.ParsedObjectPool)
		if err != nil {
			return util.StatusWrap(err, "Failed to create parsed object pool")
		}

		filePool, err := pool.NewFilePoolFromConfiguration(configuration.FilePool)
		if err != nil {
			return util.StatusWrap(err, "Failed to create file pool")
		}

		executionGRPCClient, err := grpcClientFactory.NewClientFromConfiguration(configuration.ExecutionGrpcClient, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to create execution gRPC client")
		}

		executionClientPrivateKey, err := crypto.ParsePEMWithPKCS8ECDHPrivateKey([]byte(configuration.ExecutionClientPrivateKey))
		if err != nil {
			return util.StatusWrap(err, "Failed to parse execution client private key")
		}
		executionClientCertificateChain, err := remoteexecution.ParseCertificateChain([]byte(configuration.ExecutionClientCertificateChain))
		if err != nil {
			return util.StatusWrap(err, "Failed to parse execution client certificate chain")
		}

		remoteWorkerConnection, err := grpcClientFactory.NewClientFromConfiguration(configuration.RemoteWorkerGrpcClient, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to create remote worker RPC client")
		}
		remoteWorkerClient := remoteworker_pb.NewOperationQueueClient(remoteWorkerConnection)

		platformPrivateKeys, err := remoteworker.ParsePlatformPrivateKeys(configuration.PlatformPrivateKeys)
		if err != nil {
			return err
		}
		clientCertificateVerifier, err := x509.NewClientCertificateVerifierFromConfiguration(configuration.ClientCertificateVerifier, dependenciesGroup)
		if err != nil {
			return err
		}
		workerName, err := json.Marshal(configuration.WorkerId)
		if err != nil {
			return util.StatusWrap(err, "Failed to marshal worker ID")
		}

		bzlFileBuiltins, buildFileBuiltins := model_starlark.GetBuiltins[buffered.Reference, buffered.ReferenceMetadata]()
		executor := &builderExecutor{
			objectDownloader:              objectDownloader,
			parsedObjectPool:              parsedObjectPool,
			dagUploaderClient:             dag_pb.NewUploaderClient(storageGRPCClient),
			objectContentsWalkerSemaphore: semaphore.NewWeighted(int64(runtime.NumCPU())),
			filePool:                      filePool,
			executionClient: model_executewithstorage.NewProtoClient(
				remoteexecution.NewProtoClient[*model_executewithstorage_pb.Action, model_core_pb.WeakDecodableReference, model_core_pb.WeakDecodableReference](
					remoteexecution.NewRemoteClient(
						remoteexecution_pb.NewExecutionClient(executionGRPCClient),
						executionClientPrivateKey,
						executionClientCertificateChain,
					),
				),
			),
			bzlFileBuiltins:       bzlFileBuiltins,
			buildFileBuiltins:     buildFileBuiltins,
			evaluationConcurrency: configuration.EvaluationConcurrency,
		}
		client, err := remoteworker.NewClient(
			remoteWorkerClient,
			remoteworker.NewProtoExecutor(
				model_executewithstorage.NewExecutor(executor),
			),
			clock.SystemClock,
			random.CryptoThreadSafeGenerator,
			platformPrivateKeys,
			clientCertificateVerifier,
			configuration.WorkerId,
			/* sizeClass = */ 0,
			/* isLargestSizeClass = */ true,
		)
		if err != nil {
			return util.StatusWrap(err, "Failed to create remote worker client")
		}
		remoteworker.LaunchWorkerThread(siblingsGroup, client.Run, string(workerName))

		lifecycleState.MarkReadyAndWait(siblingsGroup)
		return nil
	})
}

type builderExecutor struct {
	objectDownloader              object.Downloader[object.GlobalReference]
	parsedObjectPool              *model_parser.ParsedObjectPool
	dagUploaderClient             dag_pb.UploaderClient
	objectContentsWalkerSemaphore *semaphore.Weighted
	filePool                      pool.FilePool
	executionClient               remoteexecution.Client[*model_executewithstorage.Action[object.GlobalReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]]
	bzlFileBuiltins               starlark.StringDict
	buildFileBuiltins             starlark.StringDict
	evaluationConcurrency         int32
}

func (e *builderExecutor) CheckReadiness(ctx context.Context) error {
	return nil
}

func (e *builderExecutor) Execute(ctx context.Context, action *model_executewithstorage.Action[object.GlobalReference], executionTimeout time.Duration, executionEvents chan<- model_core.Decodable[object.LocalReference]) (model_core.Decodable[object.LocalReference], time.Duration, remoteworker_pb.CurrentState_Completed_Result, error) {
	if !proto.Equal(action.Format, &model_core_pb.ObjectFormat{
		Format: &model_core_pb.ObjectFormat_ProtoTypeName{
			ProtoTypeName: "bonanza.model.build.Action",
		},
	}) {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, status.Error(codes.InvalidArgument, "This worker cannot execute actions of this type")
	}

	actionGlobalReference := action.Reference.Value
	instanceName := actionGlobalReference.InstanceName
	referenceFormat := action.Reference.Value.GetReferenceFormat()

	actionEncoder, err := encoding.NewBinaryEncoderFromProto(
		action.Encoders,
		uint32(referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, util.StatusWrap(err, "Failed to create action encoder")
	}

	objectManager := buffered.NewObjectManager()
	objectExporter := buffered.NewObjectExporter(
		e.dagUploaderClient,
		instanceName,
		e.objectContentsWalkerSemaphore,
	)
	resultMessage := model_core.MustBuildPatchedMessage(func(resultPatcher *model_core.ReferenceMessagePatcher[buffered.ReferenceMetadata]) *model_build_pb.Result {
		var result model_build_pb.Result
		parsedObjectPoolIngester := model_parser.NewParsedObjectPoolIngester[buffered.Reference](
			e.parsedObjectPool,
			buffered.NewParsedObjectReader(
				model_parser.NewDownloadingParsedObjectReader(
					object_namespacemapping.NewNamespaceAddingDownloader(e.objectDownloader, instanceName),
				),
			),
		)
		actionReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[buffered.Reference](actionEncoder),
				model_parser.NewProtoObjectParser[buffered.Reference, model_build_pb.Action](),
			),
		)
		actionMessage, err := actionReader.ReadParsedObject(
			ctx,
			model_core.CopyDecodable(
				action.Reference,
				objectExporter.ImportReference(actionGlobalReference.LocalReference),
			),
		)
		if err != nil {
			result.Failure = &model_build_pb.Result_Failure{
				Status: status.Convert(err).Proto(),
			}
			return &result
		}
		buildSpecificationReference, err := model_core.FlattenDecodableReference(model_core.Nested(actionMessage, actionMessage.Message.BuildSpecificationReference))
		if err != nil {
			result.Failure = &model_build_pb.Result_Failure{
				Status: status.Convert(err).Proto(),
			}
			return &result
		}

		recursiveComputer := evaluation.NewRecursiveComputer(
			model_analysis.NewTypedComputer(model_analysis.NewBaseComputer[buffered.Reference, buffered.ReferenceMetadata](
				parsedObjectPoolIngester,
				buildSpecificationReference,
				actionEncoder,
				e.filePool,
				model_executewithstorage.NewObjectExportingClient(
					model_executewithstorage.NewNamespaceAddingClient(
						e.executionClient,
						instanceName,
					),
					objectExporter,
				),
				e.bzlFileBuiltins,
				e.buildFileBuiltins,
			)),
			objectManager,
		)

		buildResultKey := model_core.NewSimpleTopLevelMessage[buffered.Reference](proto.Message(&model_analysis_pb.BuildResult_Key{}))
		buildResultKeyState, err := recursiveComputer.GetOrCreateKeyState(buildResultKey)
		if err != nil {
			result.Failure = &model_build_pb.Result_Failure{
				Status: status.Convert(err).Proto(),
			}
			return &result
		}

		// Perform the build.
		var value model_core.Message[proto.Message, buffered.Reference]
		errCompute := program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			for i := int32(0); i < e.evaluationConcurrency; i++ {
				dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
					for recursiveComputer.ProcessNextQueuedKey(ctx) {
					}
					return nil
				})
			}

			var err error
			value, err = recursiveComputer.WaitForMessageValue(ctx, buildResultKeyState)
			return err
		})

		// Store all evaluation results to permit debugging of the build.
		// TODO: Use a proper configuration.
		evaluationTreeEncoder := model_encoding.NewChainedBinaryEncoder(nil)
		evaluationTreeBuilder := btree.NewSplitProllyBuilder(
			1<<16,
			1<<18,
			btree.NewObjectCreatingNodeMerger(
				evaluationTreeEncoder,
				referenceFormat,
				/* parentNodeComputer = */ func(createdObject model_core.Decodable[model_core.CreatedObject[buffered.ReferenceMetadata]], childNodes []*model_evaluation_pb.Evaluation) model_core.PatchedMessage[*model_evaluation_pb.Evaluation, buffered.ReferenceMetadata] {
					var firstKeySHA256 []byte
					switch firstEntry := childNodes[0].Level.(type) {
					case *model_evaluation_pb.Evaluation_Leaf_:
						if flattenedAny, err := model_core.FlattenAny(model_core.NewMessage(firstEntry.Leaf.Key, createdObject.Value.Contents)); err == nil {
							firstKey, _ := model_core.MarshalTopLevelMessage(flattenedAny)
							firstKeySHA256Array := sha256.Sum256(firstKey)
							firstKeySHA256 = firstKeySHA256Array[:]
						}
					case *model_evaluation_pb.Evaluation_Parent_:
						firstKeySHA256 = firstEntry.Parent.FirstKeySha256
					}
					return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[buffered.ReferenceMetadata]) *model_evaluation_pb.Evaluation {
						return &model_evaluation_pb.Evaluation{
							Level: &model_evaluation_pb.Evaluation_Parent_{
								Parent: &model_evaluation_pb.Evaluation_Parent{
									Reference:      patcher.CaptureAndAddDecodableReference(createdObject, objectManager),
									FirstKeySha256: firstKeySHA256,
								},
							},
						}
					})
				},
			),
		)
		for evaluation := range recursiveComputer.GetAllEvaluations() {
			key, err := model_core.MarshalAny(
				model_core.Patch(objectManager, evaluation.Key.Decay()),
			)
			if err != nil {
				result.Failure = &model_build_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			patcher := key.Patcher

			var value model_core.PatchedMessage[*model_core_pb.Any, buffered.ReferenceMetadata]
			if evaluation.Value.IsSet() {
				value, err = model_core.MarshalAny(
					model_core.Patch(objectManager, evaluation.Value),
				)
				if err != nil {
					result.Failure = &model_build_pb.Result_Failure{
						Status: status.Convert(err).Proto(),
					}
					return &result
				}
				patcher.Merge(value.Patcher)
			}

			dependencyTreeBuilder := btree.NewSplitProllyBuilder(
				1<<16,
				1<<18,
				btree.NewObjectCreatingNodeMerger(
					evaluationTreeEncoder,
					referenceFormat,
					/* parentNodeComputer = */ func(createdObject model_core.Decodable[model_core.CreatedObject[buffered.ReferenceMetadata]], childNodes []*model_evaluation_pb.Keys) model_core.PatchedMessage[*model_evaluation_pb.Keys, buffered.ReferenceMetadata] {
						return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[buffered.ReferenceMetadata]) *model_evaluation_pb.Keys {
							return &model_evaluation_pb.Keys{
								Level: &model_evaluation_pb.Keys_Parent_{
									Parent: &model_evaluation_pb.Keys_Parent{
										Reference: patcher.CaptureAndAddDecodableReference(createdObject, objectManager),
									},
								},
							}
						})
					},
				),
			)
			for _, dependency := range evaluation.Dependencies {
				dependencyAny, err := model_core.MarshalAny(
					model_core.Patch(objectManager, dependency.Decay()),
				)
				if err != nil {
					result.Failure = &model_build_pb.Result_Failure{
						Status: status.Convert(err).Proto(),
					}
					return &result
				}
				if err := dependencyTreeBuilder.PushChild(
					model_core.NewPatchedMessage(
						&model_evaluation_pb.Keys{
							Level: &model_evaluation_pb.Keys_Leaf{
								Leaf: dependencyAny.Message,
							},
						},
						dependencyAny.Patcher,
					),
				); err != nil {
					result.Failure = &model_build_pb.Result_Failure{
						Status: status.Convert(err).Proto(),
					}
					return &result
				}
			}
			dependencies, err := dependencyTreeBuilder.FinalizeList()
			if err != nil {
				result.Failure = &model_build_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			patcher.Merge(dependencies.Patcher)

			if err := evaluationTreeBuilder.PushChild(
				model_core.NewPatchedMessage(
					&model_evaluation_pb.Evaluation{
						Level: &model_evaluation_pb.Evaluation_Leaf_{
							Leaf: &model_evaluation_pb.Evaluation_Leaf{
								Key:          key.Message,
								Value:        value.Message,
								Dependencies: dependencies.Message,
							},
						},
					},
					patcher,
				),
			); err != nil {
				result.Failure = &model_build_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
		}

		evaluations, err := evaluationTreeBuilder.FinalizeList()
		if err != nil {
			result.Failure = &model_build_pb.Result_Failure{
				Status: status.Convert(err).Proto(),
			}
			return &result
		}
		if len(evaluations.Message) > 0 {
			createdEvaluations, err := model_core.MarshalAndEncode(
				model_core.ProtoListToMarshalable(evaluations),
				referenceFormat,
				evaluationTreeEncoder,
			)
			if err != nil {
				result.Failure = &model_build_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			result.EvaluationsReference = resultPatcher.CaptureAndAddDecodableReference(createdEvaluations, objectManager)
		}

		if errCompute != nil {
			patchedBuildResultKey := model_core.Patch(objectManager, buildResultKey.Decay())
			marshaledBuildResultKey, err := model_core.MarshalAny(patchedBuildResultKey)
			if err != nil {
				result.Failure = &model_build_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			patchedStackTraceKeys := []*model_core_pb.Any{marshaledBuildResultKey.Message}
			resultPatcher.Merge(marshaledBuildResultKey.Patcher)

			for {
				var nestedErr evaluation.NestedError[buffered.Reference]
				if !errors.As(errCompute, &nestedErr) {
					break
				}

				patchedKey := model_core.Patch(objectManager, nestedErr.Key.Decay())
				marshaledKey, err := model_core.MarshalAny(patchedKey)
				if err != nil {
					result.Failure = &model_build_pb.Result_Failure{
						Status: status.Convert(err).Proto(),
					}
					return &result
				}
				patchedStackTraceKeys = append(patchedStackTraceKeys, marshaledKey.Message)
				resultPatcher.Merge(marshaledKey.Patcher)

				errCompute = nestedErr.Err
			}

			result.Failure = &model_build_pb.Result_Failure{
				StackTraceKeys: patchedStackTraceKeys,
				Status:         status.Convert(errCompute).Proto(),
			}
			return &result
		}

		result.Failure = &model_build_pb.Result_Failure{
			Status: status.Newf(codes.Internal, "TODO: %s", value).Proto(),
		}
		return &result
	})

	createdResult, err := model_core.MarshalAndEncode(
		model_core.ProtoToMarshalable(resultMessage),
		referenceFormat,
		actionEncoder,
	)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, util.StatusWrap(err, "Failed to create marshal and encode result")
	}

	resultReference, err := objectExporter.ExportReference(
		ctx,
		objectManager.ReferenceObject(createdResult.Value.Capture(objectManager)),
	)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, util.StatusWrap(err, "Failed to export result")
	}

	resultCode := remoteworker_pb.CurrentState_Completed_SUCCEEDED
	if resultMessage.Message.Failure != nil {
		resultCode = remoteworker_pb.CurrentState_Completed_FAILED
	}
	return model_core.CopyDecodable(createdResult, resultReference), 0, resultCode, nil
}
