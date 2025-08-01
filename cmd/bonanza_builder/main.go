package main

import (
	"context"
	"crypto/ecdh"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"runtime"
	"time"

	"bonanza.build/pkg/crypto"
	"bonanza.build/pkg/evaluation"
	model_analysis "bonanza.build/pkg/model/analysis"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/encoding"
	model_encoding "bonanza.build/pkg/model/encoding"
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
	"bonanza.build/pkg/storage/dag"
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
		lifecycleState, grpcClientFactory, err := global.ApplyConfiguration(configuration.Global)
		if err != nil {
			return util.StatusWrap(err, "Failed to apply global configuration options")
		}

		storageGRPCClient, err := grpcClientFactory.NewClientFromConfiguration(configuration.StorageGrpcClient)
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

		executionGRPCClient, err := grpcClientFactory.NewClientFromConfiguration(configuration.ExecutionGrpcClient)
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

		remoteWorkerConnection, err := grpcClientFactory.NewClientFromConfiguration(configuration.RemoteWorkerGrpcClient)
		if err != nil {
			return util.StatusWrap(err, "Failed to create remote worker RPC client")
		}
		remoteWorkerClient := remoteworker_pb.NewOperationQueueClient(remoteWorkerConnection)

		platformPrivateKeys, err := remoteworker.ParsePlatformPrivateKeys(configuration.PlatformPrivateKeys)
		if err != nil {
			return err
		}
		clientCertificateVerifier, err := x509.NewClientCertificateVerifierFromConfiguration(configuration.ClientCertificateVerifier)
		if err != nil {
			return err
		}
		workerName, err := json.Marshal(configuration.WorkerId)
		if err != nil {
			return util.StatusWrap(err, "Failed to marshal worker ID")
		}

		bzlFileBuiltins, buildFileBuiltins := model_starlark.GetBuiltins[builderReference, builderReferenceMetadata]()
		executor := &builderExecutor{
			objectDownloader:              objectDownloader,
			parsedObjectPool:              parsedObjectPool,
			dagUploaderClient:             dag_pb.NewUploaderClient(storageGRPCClient),
			objectContentsWalkerSemaphore: semaphore.NewWeighted(int64(runtime.NumCPU())),
			filePool:                      filePool,
			executionClient: model_executewithstorage.NewClient(
				remoteexecution.NewProtoClient[*model_executewithstorage_pb.Action, model_core_pb.WeakDecodableReference, model_core_pb.WeakDecodableReference](
					remoteexecution.NewRemoteClient(
						remoteexecution_pb.NewExecutionClient(executionGRPCClient),
						executionClientPrivateKey,
						executionClientCertificateChain,
					),
				),
			),
			bzlFileBuiltins:   bzlFileBuiltins,
			buildFileBuiltins: buildFileBuiltins,
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

type referenceWrappingParsedObjectReader struct {
	base model_parser.ParsedObjectReader[object.LocalReference, model_core.Message[[]byte, object.LocalReference]]
}

func (r *referenceWrappingParsedObjectReader) ReadParsedObject(ctx context.Context, reference builderReference) (model_core.Message[[]byte, builderReference], error) {
	if contents := reference.embeddedMetadata.contents; contents != nil {
		// Object has not been written to storage yet.
		// Return the copy that lives in memory.
		//
		// TODO: We should return some kind of hint to indicate
		// that the caller is not permitted to cache this!
		degree := contents.GetDegree()
		outgoingReferences := make(object.OutgoingReferencesList[builderReference], 0, degree)
		children := reference.embeddedMetadata.children
		for i := range degree {
			outgoingReferences = append(outgoingReferences, builderReference{
				LocalReference:   contents.GetOutgoingReference(i),
				embeddedMetadata: children[i],
			})
		}
		return model_core.NewMessage(contents.GetPayload(), outgoingReferences), nil
	}

	// Read object from storage.
	m, err := r.base.ReadParsedObject(ctx, reference.GetLocalReference())
	if err != nil {
		return model_core.Message[[]byte, builderReference]{}, err
	}

	degree := m.OutgoingReferences.GetDegree()
	outgoingReferences := make(object.OutgoingReferencesList[builderReference], 0, degree)
	for i := range degree {
		outgoingReferences = append(outgoingReferences, builderReference{
			LocalReference: m.OutgoingReferences.GetOutgoingReference(i),
		})
	}
	return model_core.NewMessage(m.Message, outgoingReferences), nil
}

func (r *referenceWrappingParsedObjectReader) GetDecodingParametersSizeBytes() int {
	return 0
}

type builderReference struct {
	object.LocalReference
	embeddedMetadata builderReferenceMetadata
}

type builderReferenceMetadata struct {
	contents *object.Contents
	children []builderReferenceMetadata
}

func (builderReferenceMetadata) Discard()     {}
func (builderReferenceMetadata) IsCloneable() {}

func (m builderReferenceMetadata) ToObjectContentsWalker() dag.ObjectContentsWalker {
	return m
}

func (m builderReferenceMetadata) GetContents(ctx context.Context) (*object.Contents, []dag.ObjectContentsWalker, error) {
	if m.contents == nil {
		return nil, nil, status.Error(codes.Internal, "Contents for this object are not available for upload, as this object was expected to already exist")
	}

	walkers := make([]dag.ObjectContentsWalker, 0, len(m.children))
	for _, child := range m.children {
		walkers = append(walkers, child)
	}
	return m.contents, walkers, nil
}

type builderObjectCapturer struct{}

func (builderObjectCapturer) CaptureCreatedObject(createdObject model_core.CreatedObject[builderReferenceMetadata]) builderReferenceMetadata {
	return builderReferenceMetadata{
		contents: createdObject.Contents,
		children: createdObject.Metadata,
	}
}

func (builderObjectCapturer) CaptureExistingObject(reference builderReference) builderReferenceMetadata {
	if reference.embeddedMetadata.contents != nil {
		return reference.embeddedMetadata
	}
	return builderReferenceMetadata{}
}

func (builderObjectCapturer) ReferenceObject(reference object.LocalReference, metadata builderReferenceMetadata) builderReference {
	return builderReference{
		LocalReference:   reference,
		embeddedMetadata: metadata,
	}
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

	instanceName := action.Reference.Value.InstanceName
	referenceFormat := action.Reference.Value.GetReferenceFormat()

	actionEncoder, err := encoding.NewBinaryEncoderFromProto(
		action.Encoders,
		uint32(referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, util.StatusWrap(err, "Failed to create action encoder")
	}

	resultMessage := model_core.BuildPatchedMessage(func(resultPatcher *model_core.ReferenceMessagePatcher[dag.ObjectContentsWalker]) *model_build_pb.Result {
		var result model_build_pb.Result
		parsedObjectPoolIngester := model_parser.NewParsedObjectPoolIngester[builderReference](
			e.parsedObjectPool,
			&referenceWrappingParsedObjectReader{
				base: model_parser.NewDownloadingParsedObjectReader(
					object_namespacemapping.NewNamespaceAddingDownloader(e.objectDownloader, instanceName),
				),
			},
		)
		actionReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[builderReference](actionEncoder),
				model_parser.NewProtoObjectParser[builderReference, model_build_pb.Action](),
			),
		)
		actionMessage, err := actionReader.ReadParsedObject(
			ctx,
			model_core.CopyDecodable(
				action.Reference,
				builderReference{
					LocalReference: action.Reference.Value.GetLocalReference(),
				},
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

		// Perform the build.
		value, resultsIterator, stackTraceKeys, errCompute := evaluation.FullyComputeValue(
			ctx,
			model_analysis.NewTypedComputer(model_analysis.NewBaseComputer[builderReference, builderReferenceMetadata](
				parsedObjectPoolIngester,
				buildSpecificationReference,
				actionEncoder,
				e.filePool,
				&builderExecutionClient{
					base:                          e.executionClient,
					dagUploaderClient:             e.dagUploaderClient,
					objectContentsWalkerSemaphore: e.objectContentsWalkerSemaphore,
					instanceName:                  instanceName,
				},
				e.bzlFileBuiltins,
				e.buildFileBuiltins,
			)),
			model_core.NewSimpleTopLevelMessage[builderReference](proto.Message(&model_analysis_pb.BuildResult_Key{})),
			builderObjectCapturer{},
			func(localReferences object.OutgoingReferences[object.LocalReference], objectContentsWalkers []dag.ObjectContentsWalker) ([]builderReference, error) {
				degree := localReferences.GetDegree()
				storedReferences := make(object.OutgoingReferencesList[builderReference], 0, degree)
				for i := 0; i < degree; i++ {
					localReference := localReferences.GetOutgoingReference(i)
					if err := dag.UploadDAG(
						ctx,
						e.dagUploaderClient,
						instanceName.WithLocalReference(localReference),
						objectContentsWalkers[i],
						e.objectContentsWalkerSemaphore,
						// Assume everything we attempt
						// to upload is memory backed.
						object.Unlimited,
					); err != nil {
						return nil, fmt.Errorf("failed to store DAG with reference %s: %w", localReference.String(), err)
					}
					storedReferences = append(storedReferences, builderReference{
						LocalReference: localReference,
					})
				}
				return storedReferences, nil
			},
		)

		// Store all evaluation results to permit debugging of the build.
		// TODO: Use a proper configuration.
		evaluationTreeEncoder := model_encoding.NewChainedBinaryEncoder(nil)
		evaluationTreeBuilder := btree.NewSplitProllyBuilder(
			1<<16,
			1<<18,
			btree.NewObjectCreatingNodeMerger(
				evaluationTreeEncoder,
				referenceFormat,
				/* parentNodeComputer = */ func(createdObject model_core.Decodable[model_core.CreatedObject[dag.ObjectContentsWalker]], childNodes []*model_evaluation_pb.Evaluation) model_core.PatchedMessage[*model_evaluation_pb.Evaluation, dag.ObjectContentsWalker] {
					var firstKey []byte
					switch firstEntry := childNodes[0].Level.(type) {
					case *model_evaluation_pb.Evaluation_Leaf_:
						if flattenedAny, err := model_core.FlattenAny(model_core.NewMessage(firstEntry.Leaf.Key, createdObject.Value.Contents)); err == nil {
							firstKey, _ = model_core.MarshalTopLevelMessage(flattenedAny)
						}
					case *model_evaluation_pb.Evaluation_Parent_:
						firstKey = firstEntry.Parent.FirstKey
					}
					return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[dag.ObjectContentsWalker]) *model_evaluation_pb.Evaluation {
						return &model_evaluation_pb.Evaluation{
							Level: &model_evaluation_pb.Evaluation_Parent_{
								Parent: &model_evaluation_pb.Evaluation_Parent{
									Reference: patcher.CaptureAndAddDecodableReference(createdObject, model_core.WalkableCreatedObjectCapturer),
									FirstKey:  firstKey,
								},
							},
						}
					})
				},
			),
		)
		for evaluation := range resultsIterator {
			key, err := model_core.MarshalAny(
				model_core.NewPatchedMessageFromExisting(
					evaluation.Key,
					func(index int) dag.ObjectContentsWalker {
						return dag.ExistingObjectContentsWalker
					},
				),
			)
			if err != nil {
				result.Failure = &model_build_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			patcher := key.Patcher

			var value model_core.PatchedMessage[*model_core_pb.Any, dag.ObjectContentsWalker]
			if evaluation.Value.IsSet() {
				value, err = model_core.MarshalAny(
					model_core.NewPatchedMessageFromExisting(
						evaluation.Value,
						func(index int) dag.ObjectContentsWalker {
							return dag.ExistingObjectContentsWalker
						},
					),
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
					/* parentNodeComputer = */ func(createdObject model_core.Decodable[model_core.CreatedObject[dag.ObjectContentsWalker]], childNodes []*model_evaluation_pb.Dependency) model_core.PatchedMessage[*model_evaluation_pb.Dependency, dag.ObjectContentsWalker] {
						return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[dag.ObjectContentsWalker]) *model_evaluation_pb.Dependency {
							return &model_evaluation_pb.Dependency{
								Level: &model_evaluation_pb.Dependency_Parent_{
									Parent: &model_evaluation_pb.Dependency_Parent{
										Reference: patcher.CaptureAndAddDecodableReference(createdObject, model_core.WalkableCreatedObjectCapturer),
									},
								},
							}
						})
					},
				),
			)
			for _, dependency := range evaluation.Dependencies {
				dependencyAny, err := model_core.MarshalAny(
					model_core.NewPatchedMessageFromExisting(
						dependency,
						func(index int) dag.ObjectContentsWalker {
							return dag.ExistingObjectContentsWalker
						},
					),
				)
				if err != nil {
					result.Failure = &model_build_pb.Result_Failure{
						Status: status.Convert(err).Proto(),
					}
					return &result
				}
				if err := dependencyTreeBuilder.PushChild(
					model_core.NewPatchedMessage(
						&model_evaluation_pb.Dependency{
							Level: &model_evaluation_pb.Dependency_LeafKey{
								LeafKey: dependencyAny.Message,
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
			result.EvaluationsReference = resultPatcher.CaptureAndAddDecodableReference(createdEvaluations, model_core.WalkableCreatedObjectCapturer)
		}

		if errCompute != nil {
			var marshaledStackTraceKeys [][]byte
			for _, key := range stackTraceKeys {
				topLevelKey, _ := model_core.Patch(model_core.NewDiscardingObjectCapturer[builderReference](), key).SortAndSetReferences()
				keyAny, err := model_core.MarshalTopLevelAny(topLevelKey)
				if err != nil {
					result.Failure = &model_build_pb.Result_Failure{
						Status: status.Convert(err).Proto(),
					}
					return &result
				}
				marshaledKey, err := model_core.MarshalTopLevelMessage(keyAny)
				if err != nil {
					result.Failure = &model_build_pb.Result_Failure{
						Status: status.Convert(err).Proto(),
					}
					return &result
				}
				marshaledStackTraceKeys = append(marshaledStackTraceKeys, marshaledKey)
			}
			result.Failure = &model_build_pb.Result_Failure{
				StackTraceKeys: marshaledStackTraceKeys,
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
	resultReference := createdResult.Value.GetLocalReference()
	if err := dag.UploadDAG(
		ctx,
		e.dagUploaderClient,
		instanceName.WithLocalReference(resultReference),
		model_core.WalkableCreatedObjectCapturer.CaptureCreatedObject(createdResult.Value),
		e.objectContentsWalkerSemaphore,
		// Assume everything we attempt to upload is memory backed.
		object.Unlimited,
	); err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, util.StatusWrap(err, "Failed to upload result")
	}

	resultCode := remoteworker_pb.CurrentState_Completed_SUCCEEDED
	if resultMessage.Message.Failure != nil {
		resultCode = remoteworker_pb.CurrentState_Completed_FAILED
	}
	return model_core.CopyDecodable(createdResult, resultReference), 0, resultCode, nil
}

type builderExecutionClient struct {
	base                          remoteexecution.Client[*model_executewithstorage.Action[object.GlobalReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]]
	dagUploaderClient             dag_pb.UploaderClient
	objectContentsWalkerSemaphore *semaphore.Weighted
	instanceName                  object.InstanceName
}

func (e *builderExecutionClient) RunAction(ctx context.Context, platformECDHPublicKey *ecdh.PublicKey, action *model_executewithstorage.Action[builderReference], actionAdditionalData *remoteexecution_pb.Action_AdditionalData, resultReference *model_core.Decodable[builderReference], errOut *error) iter.Seq[model_core.Decodable[builderReference]] {
	// If the action is one that the caller has constructed but has
	// not been uploaded to storage yet, we need to upload it before
	// calling into the scheduler.
	if actionReference := action.Reference.Value; actionReference.embeddedMetadata.contents != nil {
		if err := dag.UploadDAG(
			ctx,
			e.dagUploaderClient,
			e.instanceName.WithLocalReference(actionReference.GetLocalReference()),
			actionReference.embeddedMetadata.ToObjectContentsWalker(),
			e.objectContentsWalkerSemaphore,
			object.Unlimited,
		); err != nil {
			*errOut = util.StatusWrap(err, "Failed to upload action")
			return func(yield func(model_core.Decodable[builderReference]) bool) {}
		}
	}

	// Re-add instance name to the action reference.
	ctxWithCancel, cancel := context.WithCancel(ctx)
	var baseResultReference model_core.Decodable[object.LocalReference]
	var baseErr error
	eventReferences := e.base.RunAction(
		ctxWithCancel,
		platformECDHPublicKey,
		&model_executewithstorage.Action[object.GlobalReference]{
			Reference: model_core.CopyDecodable(
				action.Reference,
				e.instanceName.WithLocalReference(action.Reference.Value.GetLocalReference()),
			),
			Encoders: action.Encoders,
			Format:   action.Format,
		},
		actionAdditionalData,
		&baseResultReference,
		&baseErr,
	)
	return func(yield func(model_core.Decodable[builderReference]) bool) {
		defer cancel()

		// Convert event references to native types.
		for eventReference := range eventReferences {
			if !yield(
				model_core.CopyDecodable(
					eventReference,
					builderReference{LocalReference: eventReference.Value.GetLocalReference()},
				),
			) {
				break
			}
		}

		// Convert result reference to native type.
		if baseErr != nil {
			*errOut = baseErr
			return
		}
		*resultReference = model_core.CopyDecodable(
			baseResultReference,
			builderReference{LocalReference: baseResultReference.Value.GetLocalReference()},
		)
		*errOut = nil
	}
}
