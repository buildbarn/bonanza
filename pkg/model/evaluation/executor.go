package evaluation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"time"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/core/buffered"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_parser "bonanza.build/pkg/model/parser"
	model_tag "bonanza.build/pkg/model/tag"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	model_evaluation_cache_pb "bonanza.build/pkg/proto/model/evaluation/cache"
	model_tag_pb "bonanza.build/pkg/proto/model/tag"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	dag_pb "bonanza.build/pkg/proto/storage/dag"
	"bonanza.build/pkg/remoteworker"
	"bonanza.build/pkg/storage/object"
	object_namespacemapping "bonanza.build/pkg/storage/object/namespacemapping"
	"bonanza.build/pkg/storage/tag"
	tag_namespacemapping "bonanza.build/pkg/storage/tag/namespacemapping"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/eviction"
	"github.com/buildbarn/bb-storage/pkg/program"
	eviction_pb "github.com/buildbarn/bb-storage/pkg/proto/configuration/eviction"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
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
	objectDownloader                       object.Downloader[object.GlobalReference]
	computerFactory                        ComputerFactory[buffered.Reference, *model_core.LeakCheckingReferenceMetadata[buffered.ReferenceMetadata]]
	evaluationQueuesFactory                RecursiveComputerEvaluationQueuesFactory[buffered.Reference, buffered.ReferenceMetadata]
	parsedObjectPool                       *model_parser.ParsedObjectPool
	dagUploaderClient                      dag_pb.UploaderClient
	bufferedObjectsLimit                   object.Limit
	bufferedObjectsCacheReplacementPolicy  eviction_pb.CacheReplacementPolicy
	tagResolver                            tag.Resolver[object.Namespace]
	cacheTagSignaturePrivateKey            ed25519.PrivateKey
	uploadConcurrency                      uint32
	cacheLookupResultIndirectionMaximumAge time.Duration
	clock                                  clock.Clock
}

// NewExecutor creates a remote worker that is capable of executing
// remote evaluation requests.
func NewExecutor(
	objectDownloader object.Downloader[object.GlobalReference],
	computerFactory ComputerFactory[buffered.Reference, *model_core.LeakCheckingReferenceMetadata[buffered.ReferenceMetadata]],
	evaluationQueuesFactory RecursiveComputerEvaluationQueuesFactory[buffered.Reference, buffered.ReferenceMetadata],
	parsedObjectPool *model_parser.ParsedObjectPool,
	dagUploaderClient dag_pb.UploaderClient,
	bufferedObjectsLimit object.Limit,
	bufferedObjectsCacheReplacementPolicy eviction_pb.CacheReplacementPolicy,
	tagResolver tag.Resolver[object.Namespace],
	cacheTagSignaturePrivateKey ed25519.PrivateKey,
	uploadConcurrency uint32,
	cacheLookupResultIndirectionMaximumAge time.Duration,
	clock clock.Clock,
) remoteworker.Executor[*model_executewithstorage.Action[object.GlobalReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]] {
	return &executor{
		objectDownloader:                       objectDownloader,
		computerFactory:                        computerFactory,
		evaluationQueuesFactory:                evaluationQueuesFactory,
		parsedObjectPool:                       parsedObjectPool,
		dagUploaderClient:                      dagUploaderClient,
		bufferedObjectsLimit:                   bufferedObjectsLimit,
		bufferedObjectsCacheReplacementPolicy:  bufferedObjectsCacheReplacementPolicy,
		tagResolver:                            tagResolver,
		cacheTagSignaturePrivateKey:            cacheTagSignaturePrivateKey,
		uploadConcurrency:                      uploadConcurrency,
		cacheLookupResultIndirectionMaximumAge: cacheLookupResultIndirectionMaximumAge,
		clock:                                  clock,
	}
}

func (executor) CheckReadiness(ctx context.Context) error {
	return nil
}

var actionObjectFormat = model_core.NewProtoObjectFormat(&model_evaluation_pb.Action{})

func (e *executor) Execute(ctx context.Context, action *model_executewithstorage.Action[object.GlobalReference], executionTimeout time.Duration, executionEvents chan<- model_core.Decodable[object.LocalReference]) (model_core.Decodable[object.LocalReference], time.Duration, remoteworker_pb.CurrentState_Completed_Result, error) {
	// Objects created during evaluation are buffered in memory,
	// and written to storage in the background. If writing fails,
	// evaluation is interrupted and the error is returned.
	evictionSet, err := eviction.NewSetFromConfiguration[object.LocalReference](e.bufferedObjectsCacheReplacementPolicy)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, util.StatusWrap(err, "Failed to create eviction set for buffered objects")
	}
	objectPool := buffered.NewObjectPool(
		e.bufferedObjectsLimit,
		eviction.NewMetricsSet(evictionSet, "BufferedObjectPool"),
	)
	var resultReference model_core.Decodable[object.LocalReference]
	var resultCode remoteworker_pb.CurrentState_Completed_Result
	if err := program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			return objectPool.SendQueuedObjects(ctx, e.dagUploaderClient, action.Reference.Value.GetNamespace())
		})

		var err error
		resultReference, resultCode, err = e.execute(ctx, action, executionEvents, objectPool)
		return err
	}); err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, err
	}
	return resultReference, 0, resultCode, nil
}

func (e *executor) execute(ctx context.Context, action *model_executewithstorage.Action[object.GlobalReference], executionEvents chan<- model_core.Decodable[object.LocalReference], objectPool *buffered.ObjectPool) (model_core.Decodable[object.LocalReference], remoteworker_pb.CurrentState_Completed_Result, error) {
	if !proto.Equal(action.Format, actionObjectFormat) {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, status.Error(codes.InvalidArgument, "This worker cannot execute actions of this type")
	}

	actionGlobalReference := action.Reference.Value
	instanceName := actionGlobalReference.InstanceName
	referenceFormat := action.Reference.Value.GetReferenceFormat()

	deterministicActionEncoder, err := model_encoding.NewDeterministicBinaryEncoderFromProto(
		action.Encoders,
		uint32(referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, util.StatusWrap(err, "Failed to create deterministic action encoder")
	}
	keyedActionEncoder, err := model_encoding.NewKeyedBinaryEncoderFromProto(
		action.Encoders,
		uint32(referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, util.StatusWrap(err, "Failed to create keyed action encoder")
	}

	objectManager := buffered.NewObjectManager(objectPool)
	objectExporter := buffered.ObjectExporter
	resultMessage := model_core.MustBuildPatchedMessage(func(resultPatcher *model_core.ReferenceMessagePatcher[buffered.ReferenceMetadata]) *model_evaluation_pb.Result {
		var result model_evaluation_pb.Result
		parsedObjectPoolIngester := model_parser.NewParsedObjectPoolIngester[buffered.Reference](
			e.parsedObjectPool,
			buffered.NewObjectReader(
				model_parser.NewDownloadingObjectReader(
					object_namespacemapping.NewNamespaceAddingDownloader(e.objectDownloader, instanceName),
				),
				objectPool,
			),
		)
		actionReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[buffered.Reference](deterministicActionEncoder),
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
		evaluationsReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[buffered.Reference](deterministicActionEncoder),
				model_parser.NewProtoListObjectParser[buffered.Reference, model_evaluation_pb.Evaluations](),
			),
		)
		overrides, err := model_parser.MaybeDereference(
			ctx,
			evaluationsReader,
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
		keyReferencesWithOverridesHasher := sha256.New()
		var previousKeyReferenceWithOverride []byte
		for override := range btree.AllLeaves(
			ctx,
			evaluationsReader,
			overrides,
			/* traverser = */ func(evaluation model_core.Message[*model_evaluation_pb.Evaluations, buffered.Reference]) (*model_core_pb.DecodableReference, error) {
				return evaluation.Message.GetParent().GetReference(), nil
			},
			&errIterOverrideKeys,
		) {
			overrideLeaf, ok := override.Message.Level.(*model_evaluation_pb.Evaluations_Leaf_)
			if !ok {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.New(codes.InvalidArgument, "Override is not a valid leaf").Proto(),
				}
				return &result
			}
			keyReference, err := referenceFormat.NewLocalReference(overrideLeaf.Leaf.KeyReference)
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			rawKeyReference := keyReference.GetRawReference()
			if bytes.Compare(previousKeyReferenceWithOverride, rawKeyReference) >= 0 {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.New(codes.InvalidArgument, "Overrides are not sorted by key reference, or contain duplicates").Proto(),
				}
				return &result
			}
			previousKeyReferenceWithOverride = rawKeyReference
			keyReferencesWithOverridesHasher.Write(rawKeyReference)
		}
		if errIterOverrideKeys != nil {
			result.Failure = &model_evaluation_pb.Result_Failure{
				Status: status.Convert(errIterOverrideKeys).Proto(),
			}
			return &result
		}

		cacheTagSignaturePublicKey := e.cacheTagSignaturePrivateKey.Public().(ed25519.PublicKey)
		cacheTagSignaturePKIXPublicKey, err := x509.MarshalPKIXPublicKey(cacheTagSignaturePublicKey)
		if err != nil {
			result.Failure = &model_evaluation_pb.Result_Failure{
				Status: status.Convert(err).Proto(),
			}
			return &result
		}

		actionTagKeyData, _ := model_core.MustBuildPatchedMessage(
			func(patcher *model_core.ReferenceMessagePatcher[model_core.NoopReferenceMetadata]) *model_evaluation_cache_pb.ActionTagKeyData {
				keyReferencesWithOverridesHash := keyReferencesWithOverridesHasher.Sum(nil)
				return &model_evaluation_cache_pb.ActionTagKeyData{
					CommonTagKeyData: &model_tag_pb.CommonKeyData{
						SignaturePublicKey: cacheTagSignaturePKIXPublicKey,
						ReferenceFormat:    referenceFormat.ToProto(),
						ObjectEncoders:     action.Encoders,
					},
					KeyReferencesWithOverridesHash: keyReferencesWithOverridesHash,
				}
			},
		).SortAndSetReferences()
		actionTagKeyReference, err := model_core.ComputeTopLevelMessageReference(
			actionTagKeyData,
			referenceFormat,
		)

		queues := e.evaluationQueuesFactory.NewQueues()
		keysReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[buffered.Reference](deterministicActionEncoder),
				model_parser.NewProtoListObjectParser[buffered.Reference, model_evaluation_pb.Keys](),
			),
		)
		evaluationReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[buffered.Reference](deterministicActionEncoder),
				model_parser.NewProtoObjectParser[buffered.Reference, model_evaluation_pb.Evaluation](),
			),
		)
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
			model_tag.NewBoundStore(
				model_tag.NewObjectImportingBoundResolver(
					model_tag.NewStorageBackedBoundResolver(
						tag_namespacemapping.NewNamespaceAddingResolver(
							e.tagResolver,
							object.Namespace{
								InstanceName:    instanceName,
								ReferenceFormat: referenceFormat,
							},
						),
						*(*[ed25519.PublicKeySize]byte)(cacheTagSignaturePublicKey),
					),
					objectExporter,
				),
				buffered.NewTagBoundUpdater(
					objectPool,
					e.cacheTagSignaturePrivateKey,
					e.clock,
				),
			),
			actionTagKeyReference,
			evaluationReader,
			model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					model_parser.NewEncodedObjectParser[buffered.Reference](keyedActionEncoder),
					model_parser.NewProtoObjectParser[buffered.Reference, model_evaluation_cache_pb.LookupResult](),
				),
			),
			keysReader,
			deterministicActionEncoder,
			keyedActionEncoder,
			e.clock,
			e.cacheLookupResultIndirectionMaximumAge,
		)

		// Create KeyState for keys for which overrides are present.
		var errIterRegisterOverrides error
		for override := range btree.AllLeaves(
			ctx,
			evaluationsReader,
			overrides,
			/* traverser = */ func(evaluation model_core.Message[*model_evaluation_pb.Evaluations, buffered.Reference]) (*model_core_pb.DecodableReference, error) {
				return evaluation.Message.GetParent().GetReference(), nil
			},
			&errIterRegisterOverrides,
		) {
			overrideLeaf, ok := override.Message.Level.(*model_evaluation_pb.Evaluations_Leaf_)
			if !ok {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.New(codes.InvalidArgument, "Override is not a valid leaf").Proto(),
				}
				return &result
			}
			keyReference, err := referenceFormat.NewLocalReference(overrideLeaf.Leaf.KeyReference)
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			evaluation, err := GraphletGetEvaluation(ctx, evaluationReader, model_core.Nested(override, overrideLeaf.Leaf.Graphlet))
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			value, err := model_core.FlattenAny(model_core.Nested(evaluation, evaluation.Message.Value))
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			if err := recursiveComputer.OverrideKeyState(keyReference, value); err != nil {
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
		var errIterRequestedKeys error
		var requestedKeys []model_core.TopLevelMessage[*anypb.Any, buffered.Reference]
		var requestedKeyReferences []object.LocalReference
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

			requestedKey, err := model_core.FlattenAny(model_core.Nested(requestedKeyNode, requestedKeyLeaf.Leaf))
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			requestedKeys = append(requestedKeys, requestedKey)

			requestedKeyReference, err := model_core.ComputeTopLevelMessageReference(requestedKey, referenceFormat)
			if err != nil {
				result.Failure = &model_evaluation_pb.Result_Failure{
					Status: status.Convert(err).Proto(),
				}
				return &result
			}
			requestedKeyReferences = append(requestedKeyReferences, requestedKeyReference)

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
		errComputeAndUpload := program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			// Launch a goroutine for reporting progress.
			dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				for {
					t, tChan := e.clock.NewTimer(2 * time.Second)
					select {
					case <-ctx.Done():
						t.Stop()
						return nil
					case <-tChan:
					}

					progress, err := recursiveComputer.GetProgress(ctx)
					if err != nil {
						return err
					}
					createdProgress, err := model_core.MarshalAndEncodeDeterministic(
						model_core.ProtoToBinaryMarshaler(progress),
						referenceFormat,
						deterministicActionEncoder,
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

			// Launch goroutines for uploading evaluation
			// results, so that subsequent builds can skip
			// evaluation.
			for i := uint32(0); i < e.uploadConcurrency; i++ {
				siblingsGroup.Go(func(ctx context.Context, siblingsGroup, group program.Group) error {
					for {
						if shouldContinue, err := recursiveComputer.ProcessNextUploadableKey(ctx); err != nil {
							return util.StatusWrap(err, "Failed to upload evaluation results")
						} else if !shouldContinue {
							return nil
						}
					}
				})
			}

			return program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
				// Launch goroutines for performing evaluation.
				queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)

				// Launch goroutines for waiting for build completion.
				for i, requestedKeyState := range requestedKeyStates {
					siblingsGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
						if err := recursiveComputer.WaitForEvaluation(ctx, requestedKeyState); err != nil {
							return NestedError[buffered.Reference, buffered.ReferenceMetadata]{
								KeyState: requestedKeyStates[i],
								Err:      err,
							}
						}
						return nil
					})
				}

				// Once we've gathered all of the values we
				// are waiting for, we may also stop
				// uploading evaluation results.
				//
				// TODO: This nesting of program.RunLocal()
				// feels and calling this method against
				// recursiveComputer feels a bit convoluted.
				// Is there a way to simplify?
				dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
					<-ctx.Done()
					recursiveComputer.GracefullyStopUploading()
					return nil
				})
				return nil
			})
		})

		outcomes, err := recursiveComputer.GetEvaluations(ctx, requestedKeyStates)
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
				deterministicActionEncoder,
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

		if errComputeAndUpload != nil {
			var patchedStackTraceKeys []*model_core_pb.Any
			for {
				var nestedErr NestedError[buffered.Reference, buffered.ReferenceMetadata]
				if !errors.As(errComputeAndUpload, &nestedErr) {
					break
				}

				key, err := recursiveComputer.GetKeyStateKeyMessage(ctx, nestedErr.KeyState)
				if err != nil {
					result.Failure = &model_evaluation_pb.Result_Failure{
						Status: status.Convert(err).Proto(),
					}
					return &result
				}
				patchedStackTraceKeys = append(
					patchedStackTraceKeys,
					model_core.Patch(
						objectManager,
						model_core.WrapTopLevelAny(key).Decay(),
					).Merge(resultPatcher),
				)

				errComputeAndUpload = nestedErr.Err
			}

			result.Failure = &model_evaluation_pb.Result_Failure{
				StackTraceKeys: patchedStackTraceKeys,
				Status:         status.Convert(errComputeAndUpload).Proto(),
			}
			return &result
		}
		return &result
	})

	createdResult, err := model_core.MarshalAndEncodeDeterministic(
		model_core.ProtoToBinaryMarshaler(resultMessage),
		referenceFormat,
		deterministicActionEncoder,
	)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, util.StatusWrap(err, "Failed to create marshal and encode result")
	}
	capturedResult, err := createdResult.Value.Capture(ctx, objectManager)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, util.StatusWrap(err, "Failed to capture result")
	}

	resultReference, err := objectExporter.ExportReference(ctx, objectManager.ReferenceObject(capturedResult))
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, util.StatusWrap(err, "Failed to export result")
	}

	// Tags are created asynchronously. Wait for them to be written,
	// so that subsequent builds can make use of the cached
	// evaluation results.
	if err := objectPool.FlushTags(ctx); err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, util.StatusWrap(err, "Failed to write cached evaluation results")
	}

	resultCode := remoteworker_pb.CurrentState_Completed_SUCCEEDED
	if resultMessage.Message.Failure != nil {
		resultCode = remoteworker_pb.CurrentState_Completed_FAILED
	}
	return model_core.CopyDecodable(createdResult, resultReference), resultCode, nil
}
