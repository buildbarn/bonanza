package evaluation

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"time"

	"bonanza.build/pkg/crypto/lthash"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/core/buffered"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_parser "bonanza.build/pkg/model/parser"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_encoding_pb "bonanza.build/pkg/proto/model/encoding"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	model_evaluation_cache_pb "bonanza.build/pkg/proto/model/evaluation/cache"
	model_tag_pb "bonanza.build/pkg/proto/model/tag"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	"bonanza.build/pkg/remoteworker"
	"bonanza.build/pkg/storage/dag"
	dag_namespacemapping "bonanza.build/pkg/storage/dag/namespacemapping"
	"bonanza.build/pkg/storage/object"
	object_namespacemapping "bonanza.build/pkg/storage/object/namespacemapping"
	"bonanza.build/pkg/storage/tag"
	tag_namespacemapping "bonanza.build/pkg/storage/tag/namespacemapping"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ComputerFactory is called into by the executor to obtain an instance
// of Computer whenever an evaluation request is received.
type ComputerFactory[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	NewComputer(
		namespace object.Namespace,
		parsedObjectPoolIngester *model_parser.ParsedObjectPoolIngester[TReference],
		objectExporter model_core.ObjectExporter[TReference, object.LocalReference],
	) Computer[TReference, TMetadata]
}

type executor struct {
	objectDownloader            object.Downloader[object.GlobalReference]
	computerFactory             ComputerFactory[buffered.Reference, *model_core.LeakCheckingReferenceMetadata[buffered.ReferenceMetadata]]
	queuesFactory               RecursiveComputerQueuesFactory[buffered.Reference, buffered.ReferenceMetadata]
	parsedObjectPool            *model_parser.ParsedObjectPool
	dagUploader                 dag.Uploader[object.InstanceName, object.GlobalReference]
	tagResolver                 tag.Resolver[object.Namespace]
	cacheTagSignaturePrivateKey ed25519.PrivateKey
	clock                       clock.Clock
}

// NewExecutor creates a remote worker that is capable of executing
// remote evaluation requests.
func NewExecutor(
	objectDownloader object.Downloader[object.GlobalReference],
	computerFactory ComputerFactory[buffered.Reference, *model_core.LeakCheckingReferenceMetadata[buffered.ReferenceMetadata]],
	queuesFactory RecursiveComputerQueuesFactory[buffered.Reference, buffered.ReferenceMetadata],
	parsedObjectPool *model_parser.ParsedObjectPool,
	dagUploader dag.Uploader[object.InstanceName, object.GlobalReference],
	tagResolver tag.Resolver[object.Namespace],
	cacheTagSignaturePrivateKey ed25519.PrivateKey,
	clock clock.Clock,
) remoteworker.Executor[*model_executewithstorage.Action[object.GlobalReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]] {
	return &executor{
		objectDownloader:            objectDownloader,
		computerFactory:             computerFactory,
		queuesFactory:               queuesFactory,
		parsedObjectPool:            parsedObjectPool,
		dagUploader:                 dagUploader,
		tagResolver:                 tagResolver,
		cacheTagSignaturePrivateKey: cacheTagSignaturePrivateKey,
		clock:                       clock,
	}
}

func (executor) CheckReadiness(ctx context.Context) error {
	return nil
}

var actionObjectFormat = model_core.NewProtoObjectFormat(&model_evaluation_pb.Action{})

func (e *executor) Execute(ctx context.Context, action *model_executewithstorage.Action[object.GlobalReference], executionTimeout time.Duration, executionEvents chan<- model_core.Decodable[object.LocalReference]) (model_core.Decodable[object.LocalReference], time.Duration, remoteworker_pb.CurrentState_Completed_Result, error) {
	if !proto.Equal(action.Format, actionObjectFormat) {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, status.Error(codes.InvalidArgument, "This worker cannot execute actions of this type")
	}

	actionGlobalReference := action.Reference.Value
	instanceName := actionGlobalReference.InstanceName
	referenceFormat := action.Reference.Value.GetReferenceFormat()

	actionEncoder, err := model_encoding.NewDeterministicBinaryEncoderFromProto(
		action.Encoders,
		uint32(referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, util.StatusWrap(err, "Failed to create action encoder")
	}

	objectManager := buffered.NewObjectManager()
	objectExporter := buffered.NewObjectExporter(
		dag_namespacemapping.NewNamespaceAddingUploader(e.dagUploader, instanceName),
	)
	resultMessage := model_core.MustBuildPatchedMessage(func(resultPatcher *model_core.ReferenceMessagePatcher[buffered.ReferenceMetadata]) *model_evaluation_pb.Result {
		var result model_evaluation_pb.Result
		parsedObjectPoolIngester := model_parser.NewParsedObjectPoolIngester[buffered.Reference](
			e.parsedObjectPool,
			buffered.NewObjectReader(
				model_parser.NewDownloadingObjectReader(
					object_namespacemapping.NewNamespaceAddingDownloader(e.objectDownloader, instanceName),
				),
			),
		)
		actionReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[buffered.Reference](actionEncoder),
				model_parser.NewProtoObjectParser[buffered.Reference, model_evaluation_pb.Action](),
			),
		)
		actionMessage, err := actionReader.ReadObject(
			ctx,
			model_core.CopyDecodable(
				action.Reference,
				objectExporter.ImportReference(actionGlobalReference.LocalReference),
			),
		)
		if err != nil {
			result.Failure = &model_evaluation_pb.Result_Failure{
				Status: status.Convert(err).Proto(),
			}
			return &result
		}

		// Keys for which we have overrides in place.
		evaluationReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[buffered.Reference](actionEncoder),
				model_parser.NewProtoListObjectParser[buffered.Reference, model_evaluation_pb.Evaluation](),
			),
		)
		overrides, err := model_parser.MaybeDereference(
			ctx,
			evaluationReader,
			model_core.Nested(actionMessage, actionMessage.Message.OverridesReference),
		)
		if err != nil {
			result.Failure = &model_evaluation_pb.Result_Failure{
				Status: status.Convert(err).Proto(),
			}
			return &result
		}

		// Compute a hash of all the keys for which overrides
		// are present, as this determines the shape of the
		// build graph. This hash needs to be included in the
		// hashes of cache tags.
		var errIterOverrideKeys error
		keysWithOverridesHasher := lthash.NewHasher()
		for override := range btree.AllLeaves(
			ctx,
			evaluationReader,
			overrides,
			/* traverser = */ func(evaluation model_core.Message[*model_evaluation_pb.Evaluation, buffered.Reference]) (*model_core_pb.DecodableReference, error) {
				return evaluation.Message.GetParent().GetReference(), nil
			},
			&errIterOverrideKeys,
		) {
			overrideLeaf, ok := override.Message.Level.(*model_evaluation_pb.Evaluation_Leaf_)
			if !ok {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.New(codes.InvalidArgument, "Override is not a valid leaf").Proto(),
				}
				return &result
			}
			key, err := model_core.FlattenAny(model_core.Nested(override, overrideLeaf.Leaf.Key))
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			marshaledKey, err := model_core.MarshalTopLevelMessage(key)
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			keysWithOverridesHasher.Add(marshaledKey)
		}
		if errIterOverrideKeys != nil {
			result.Failure = &model_evaluation_pb.Result_Failure{
				Status: status.Convert(errIterOverrideKeys).Proto(),
			}
			return &result
		}

		cacheTagSignaturePublicKey, err := x509.MarshalPKIXPublicKey(e.cacheTagSignaturePrivateKey.Public())
		if err != nil {
			result.Failure = &model_evaluation_pb.Result_Failure{
				Status: status.Convert(err).Proto(),
			}
			return &result
		}

		// TODO: Set proper encoders!
		var cacheObjectEncoders []*model_encoding_pb.BinaryEncoder
		actionTagKeyData, _ := model_core.MustBuildPatchedMessage(
			func(patcher *model_core.ReferenceMessagePatcher[model_core.NoopReferenceMetadata]) *model_evaluation_cache_pb.ActionTagKeyData {
				keysWithOverridesHash := keysWithOverridesHasher.Sum(nil)
				return &model_evaluation_cache_pb.ActionTagKeyData{
					CommonTagKeyData: &model_tag_pb.CommonKeyData{
						SignaturePublicKey: cacheTagSignaturePublicKey,
						ReferenceFormat:    referenceFormat.ToProto(),
						ObjectEncoders:     cacheObjectEncoders,
					},
					KeysWithOverridesHash: keysWithOverridesHash[:],
				}
			},
		).SortAndSetReferences()
		actionTagKeyReference, err := model_core.ComputeTopLevelMessageReference(
			actionTagKeyData,
			referenceFormat,
		)

		queues := e.queuesFactory.NewQueues()
		recursiveComputer := NewRecursiveComputer(
			NewLeakCheckingComputer(
				e.computerFactory.NewComputer(
					action.Reference.Value.GetNamespace(),
					parsedObjectPoolIngester,
					objectExporter,
				),
			),
			queues,
			referenceFormat,
			objectManager,
			tag_namespacemapping.NewNamespaceAddingResolver(
				e.tagResolver,
				object.Namespace{
					InstanceName:    instanceName,
					ReferenceFormat: referenceFormat,
				},
			),
			actionTagKeyReference,
			e.cacheTagSignaturePrivateKey,
			model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				// TODO: Encode objects.
				model_parser.NewProtoObjectParser[buffered.Reference, model_evaluation_cache_pb.LookupResult](),
			),
			e.clock,
		)

		// Create KeyState for keys for which overrides are present.
		var errIterRegisterOverrides error
		for override := range btree.AllLeaves(
			ctx,
			evaluationReader,
			overrides,
			/* traverser = */ func(evaluation model_core.Message[*model_evaluation_pb.Evaluation, buffered.Reference]) (*model_core_pb.DecodableReference, error) {
				return evaluation.Message.GetParent().GetReference(), nil
			},
			&errIterRegisterOverrides,
		) {
			overrideLeaf, ok := override.Message.Level.(*model_evaluation_pb.Evaluation_Leaf_)
			if !ok {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.New(codes.InvalidArgument, "Override is not a valid leaf").Proto(),
				}
				return &result
			}
			key, err := model_core.UnmarshalAnyNew(model_core.Nested(override, overrideLeaf.Leaf.Key))
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			value, err := model_core.UnmarshalAnyNew(model_core.Nested(override, overrideLeaf.Leaf.Value))
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			if err := recursiveComputer.InjectKeyState(key, value.Decay()); err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
		}
		if errIterRegisterOverrides != nil {
			result.Failure = &model_evaluation_pb.Result_Failure{
				Status: status.Convert(errIterRegisterOverrides).Proto(),
			}
			return &result
		}

		// Determine which keys are requested. For each of them
		// create a KeyState so that its value will be computed.
		keysReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[buffered.Reference](actionEncoder),
				model_parser.NewProtoListObjectParser[buffered.Reference, model_evaluation_pb.Keys](),
			),
		)
		var errIterRequestedKeys error
		var requestedKeys []model_core.TopLevelMessage[proto.Message, buffered.Reference]
		for requestedKeyNode := range btree.AllLeaves(
			ctx,
			keysReader,
			model_core.Nested(actionMessage, actionMessage.Message.RequestedKeys),
			/* traverser = */ func(evaluation model_core.Message[*model_evaluation_pb.Keys, buffered.Reference]) (*model_core_pb.DecodableReference, error) {
				return evaluation.Message.GetParent().GetReference(), nil
			},
			&errIterRequestedKeys,
		) {
			requestedKeyLeaf, ok := requestedKeyNode.Message.Level.(*model_evaluation_pb.Keys_Leaf)
			if !ok {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.New(codes.InvalidArgument, "Key is not a valid leaf").Proto(),
				}
				return &result
			}
			requestedKey, err := model_core.UnmarshalAnyNew(model_core.Nested(requestedKeyNode, requestedKeyLeaf.Leaf))
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			requestedKeys = append(requestedKeys, requestedKey)
		}
		if errIterRequestedKeys != nil {
			result.Failure = &model_evaluation_pb.Result_Failure{
				Status: status.Convert(errIterRequestedKeys).Proto(),
			}
			return &result
		}

		var requestedKeyStates []*KeyState[buffered.Reference, buffered.ReferenceMetadata]
		for _, requestedKey := range requestedKeys {
			keyState, err := recursiveComputer.GetOrCreateKeyState(requestedKey)
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			requestedKeyStates = append(requestedKeyStates, keyState)
		}

		// Perform the build.
		var value model_core.Message[proto.Message, buffered.Reference]
		errCompute := program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			// Launch a goroutine for reporting progress.
			dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				for {
					t, tChan := e.clock.NewTimer(10 * time.Second)
					select {
					case <-ctx.Done():
						t.Stop()
						return nil
					case <-tChan:
					}

					progress, err := recursiveComputer.GetProgress()
					if err != nil {
						return err
					}
					createdProgress, err := model_core.MarshalAndEncodeDeterministic(
						model_core.ProtoToBinaryMarshaler(progress),
						referenceFormat,
						actionEncoder,
					)
					if err != nil {
						return err
					}
					capturedProgress, err := createdProgress.Value.Capture(ctx, objectManager)
					if err != nil {
						if ctx.Err() != nil {
							return nil
						}
						return err
					}
					progressReference, err := objectExporter.ExportReference(ctx, objectManager.ReferenceObject(capturedProgress))
					if err != nil {
						if ctx.Err() != nil {
							return nil
						}
						return err
					}

					select {
					case <-ctx.Done():
					case executionEvents <- model_core.CopyDecodable(createdProgress, progressReference):
					}
				}
			})

			// Launch goroutines for performing evaluation.
			queues.ProcessAllQueuedKeys(dependenciesGroup, recursiveComputer)

			// Launch goroutines for waiting for build completion.
			for i, requestedKeyState := range requestedKeyStates {
				siblingsGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
					if _, err := recursiveComputer.WaitForMessageValue(ctx, requestedKeyState); err != nil {
						return NestedError[buffered.Reference]{
							Key: requestedKeys[i],
							Err: err,
						}
					}
					return nil
				})
			}
			return nil
		})

		// Store all evaluation results to permit debugging of the build.
		// TODO: Use a proper configuration.
		evaluationTreeEncoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
		outcomesTreeBuilder := btree.NewHeightAwareBuilder(
			btree.NewProllyChunkerFactory[buffered.ReferenceMetadata](
				/* minimumSizeBytes = */ 1<<16,
				/* maximumSizeBytes = */ 1<<18,
				/* isParent = */ func(evaluation *model_evaluation_pb.Evaluation) bool {
					return evaluation.GetParent() != nil
				},
			),
			btree.NewObjectCreatingNodeMerger(
				evaluationTreeEncoder,
				referenceFormat,
				/* parentNodeComputer = */ btree.Capturing(ctx, objectManager, func(createdObject model_core.Decodable[model_core.MetadataEntry[buffered.ReferenceMetadata]], childNodes model_core.Message[[]*model_evaluation_pb.Evaluation, object.LocalReference]) model_core.PatchedMessage[*model_evaluation_pb.Evaluation, buffered.ReferenceMetadata] {
					var firstKeyReference []byte
					switch firstEntry := childNodes.Message[0].Level.(type) {
					case *model_evaluation_pb.Evaluation_Leaf_:
						if flattenedAny, err := model_core.FlattenAny(model_core.Nested(childNodes, firstEntry.Leaf.Key)); err == nil {
							if r, err := model_core.ComputeTopLevelMessageReference(flattenedAny, referenceFormat); err == nil {
								firstKeyReference = r.GetRawReference()
							}
						}
					case *model_evaluation_pb.Evaluation_Parent_:
						firstKeyReference = firstEntry.Parent.FirstKeyReference
					}
					return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[buffered.ReferenceMetadata]) *model_evaluation_pb.Evaluation {
						return &model_evaluation_pb.Evaluation{
							Level: &model_evaluation_pb.Evaluation_Parent_{
								Parent: &model_evaluation_pb.Evaluation_Parent{
									Reference:         patcher.AddDecodableReference(createdObject),
									FirstKeyReference: firstKeyReference,
								},
							},
						}
					})
				}),
			),
		)
		defer outcomesTreeBuilder.Discard()

		for evaluation := range recursiveComputer.GetAllEvaluations() {
			key, err := model_core.MarshalAny(
				model_core.Patch(objectManager, evaluation.Key.Decay()),
			)
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
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
					result.Failure = &model_evaluation_pb.Result_Failure{
						Status: status.Convert(err).Proto(),
					}
					return &result
				}
				patcher.Merge(value.Patcher)
			}

			dependencyTreeBuilder := btree.NewHeightAwareBuilder(
				btree.NewProllyChunkerFactory[buffered.ReferenceMetadata](
					/* minimumSizeBytes = */ 1<<16,
					/* minimumSizeBytes = */ 1<<18,
					/* isParent = */ func(keys *model_evaluation_pb.Keys) bool {
						return keys.GetParent() != nil
					},
				),
				btree.NewObjectCreatingNodeMerger(
					evaluationTreeEncoder,
					referenceFormat,
					/* parentNodeComputer = */ btree.Capturing(ctx, objectManager, func(createdObject model_core.Decodable[model_core.MetadataEntry[buffered.ReferenceMetadata]], childNodes model_core.Message[[]*model_evaluation_pb.Keys, object.LocalReference]) model_core.PatchedMessage[*model_evaluation_pb.Keys, buffered.ReferenceMetadata] {
						return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[buffered.ReferenceMetadata]) *model_evaluation_pb.Keys {
							return &model_evaluation_pb.Keys{
								Level: &model_evaluation_pb.Keys_Parent_{
									Parent: &model_evaluation_pb.Keys_Parent{
										Reference: patcher.AddDecodableReference(createdObject),
									},
								},
							}
						})
					}),
				),
			)
			for _, dependency := range evaluation.Dependencies {
				dependencyAny, err := model_core.MarshalAny(
					model_core.Patch(objectManager, dependency.Decay()),
				)
				if err != nil {
					result.Failure = &model_evaluation_pb.Result_Failure{
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
					result.Failure = &model_evaluation_pb.Result_Failure{
						Status: status.Convert(err).Proto(),
					}
					return &result
				}
			}
			dependencies, err := dependencyTreeBuilder.FinalizeList()
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			patcher.Merge(dependencies.Patcher)

			if err := outcomesTreeBuilder.PushChild(
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
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
		}

		outcomes, err := outcomesTreeBuilder.FinalizeList()
		if err != nil {
			result.Failure = &model_evaluation_pb.Result_Failure{
				Status: status.Convert(err).Proto(),
			}
			return &result
		}
		if len(outcomes.Message) > 0 {
			createdEvaluations, err := model_core.MarshalAndEncodeDeterministic(
				model_core.ProtoListToBinaryMarshaler(outcomes),
				referenceFormat,
				evaluationTreeEncoder,
			)
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			outcomesReference, err := resultPatcher.CaptureAndAddDecodableReference(ctx, createdEvaluations, objectManager)
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			result.OutcomesReference = outcomesReference
		}

		if errCompute != nil {
			var patchedStackTraceKeys []*model_core_pb.Any
			for {
				var nestedErr NestedError[buffered.Reference]
				if !errors.As(errCompute, &nestedErr) {
					break
				}

				patchedKey := model_core.Patch(objectManager, nestedErr.Key.Decay())
				marshaledKey, err := model_core.MarshalAny(patchedKey)
				if err != nil {
					result.Failure = &model_evaluation_pb.Result_Failure{
						Status: status.Convert(err).Proto(),
					}
					return &result
				}
				patchedStackTraceKeys = append(patchedStackTraceKeys, marshaledKey.Message)
				resultPatcher.Merge(marshaledKey.Patcher)

				errCompute = nestedErr.Err
			}

			result.Failure = &model_evaluation_pb.Result_Failure{
				StackTraceKeys: patchedStackTraceKeys,
				Status:         status.Convert(errCompute).Proto(),
			}
			return &result
		}

		result.Failure = &model_evaluation_pb.Result_Failure{
			Status: status.Newf(codes.Internal, "TODO: %s", value).Proto(),
		}
		return &result
	})

	createdResult, err := model_core.MarshalAndEncodeDeterministic(
		model_core.ProtoToBinaryMarshaler(resultMessage),
		referenceFormat,
		actionEncoder,
	)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, util.StatusWrap(err, "Failed to create marshal and encode result")
	}
	capturedResult, err := createdResult.Value.Capture(ctx, objectManager)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, util.StatusWrap(err, "Failed to capture result")
	}

	resultReference, err := objectExporter.ExportReference(ctx, objectManager.ReferenceObject(capturedResult))
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
