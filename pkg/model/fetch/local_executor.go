package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"hash"
	"io"
	"math"
	"sync/atomic"
	"time"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_format "bonanza.build/pkg/model/format"
	model_parser "bonanza.build/pkg/model/parser"
	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	"bonanza.build/pkg/remoteworker"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"
	object_namespacemapping "bonanza.build/pkg/storage/object/namespacemapping"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/pool"
	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type localExecutor struct {
	objectDownloader object.Downloader[object.GlobalReference]
	parsedObjectPool *model_parser.ParsedObjectPool
	dagUploader      dag.Uploader[object.InstanceName, object.GlobalReference]
	fetcher          Fetcher
	filePool         pool.FilePool

	binaryEncoderFactory model_encoding.BinaryEncoderFactory
	clock                clock.Clock
}

// NewLocalExecutor creates a remote worker protocol executor that is
// capable of processing "fetch" actions. URLs listed in the action are
// fetched using a HTTP client, and the resulting file is uploaded to
// storage.
func NewLocalExecutor(
	objectDownloader object.Downloader[object.GlobalReference],
	parsedObjectPool *model_parser.ParsedObjectPool,
	dagUploader dag.Uploader[object.InstanceName, object.GlobalReference],
	fetcher Fetcher,
	filePool pool.FilePool,
	binaryEncoderFactory model_encoding.BinaryEncoderFactory,
	clock clock.Clock,
) remoteworker.Executor[*model_executewithstorage.NamespacedAction, *model_core.Decodable[object.LocalReference], *model_core.Decodable[object.LocalReference]] {
	return &localExecutor{
		objectDownloader: objectDownloader,
		parsedObjectPool: parsedObjectPool,
		dagUploader:      dagUploader,
		fetcher:          fetcher,
		filePool:         filePool,

		binaryEncoderFactory: binaryEncoderFactory,
		clock:                clock,
	}
}

func (localExecutor) CheckReadiness(ctx context.Context) error {
	return nil
}

var actionObjectFormat = model_format.NewProto[model_fetch_pb.Action]().ToProto()

// uploadMessage stores a message in an object, and uploads it to
// storage. As objects cannot be empty, no reference is returned if the
// message is empty.
func uploadMessage[TMessage any, TMessagePtr interface {
	*TMessage
	proto.Message
}](
	ctx context.Context,
	e *localExecutor,
	namespace object.Namespace,
	actionEncoder model_encoding.DeterministicBinaryEncoder,
	message model_core.PatchedMessage[TMessagePtr, dag.ObjectContentsWalker],
) (*model_core.Decodable[object.LocalReference], error) {
	if proto.Size(message.Message) == 0 {
		message.Discard()
		return nil, nil
	}
	createdObject, err := model_core.MarshalAndEncodeDeterministic(
		message.ToMarshaler(model_format.NewProto[TMessage, TMessagePtr]()),
		namespace.ReferenceFormat,
		actionEncoder,
	)
	if err != nil {
		return nil, util.StatusWrap(err, "Failed to marshal and encode message")
	}
	localReference := createdObject.Value.GetLocalReference()
	if err := e.dagUploader.UploadDAG(
		ctx,
		namespace.WithLocalReference(localReference),
		dag.NewSimpleObjectContentsWalker(
			createdObject.Value.Contents,
			createdObject.Value.Metadata,
		),
	); err != nil {
		return nil, util.StatusWrap(err, "Failed to upload message")
	}
	decodableReference := model_core.CopyDecodable(createdObject, localReference)
	return &decodableReference, nil
}

// countingReader is a decorator for io.Reader that keeps track of the
// number of bytes read.
type countingReader struct {
	base      io.Reader
	bytesRead *atomic.Uint64
}

func (r countingReader) Read(p []byte) (int, error) {
	n, err := r.base.Read(p)
	r.bytesRead.Add(uint64(n))
	return n, err
}

// copyWithProgress copies the contents of a file that is being
// downloaded to a writer. While copying, execution events are
// periodically emitted to report how many bytes have been downloaded.
func (e *localExecutor) copyWithProgress(
	ctx context.Context,
	w io.Writer,
	body io.Reader,
	url string,
	sizeBytes int64,
	namespace object.Namespace,
	actionEncoder model_encoding.DeterministicBinaryEncoder,
	executionEvents chan<- *model_core.Decodable[object.LocalReference],
) error {
	var bytesRead atomic.Uint64
	reportingCtx, cancelReporting := context.WithCancel(ctx)
	reportingDone := make(chan struct{})
	go func() {
		defer close(reportingDone)
		for {
			t, tChan := e.clock.NewTimer(time.Second)
			select {
			case <-reportingCtx.Done():
				t.Stop()
				return
			case <-tChan:
			}

			event := &model_fetch_pb.Event{
				Url:                 url,
				DownloadedSizeBytes: bytesRead.Load(),
			}
			if sizeBytes > 0 {
				event.TotalSizeBytes = uint64(sizeBytes)
			}
			eventReference, err := uploadMessage(
				reportingCtx,
				e,
				namespace,
				actionEncoder,
				model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](event),
			)
			if err != nil {
				// Failing to report progress should not
				// cause the download to fail.
				continue
			}
			select {
			case <-reportingCtx.Done():
				return
			case executionEvents <- eventReference:
			}
		}
	}()

	_, err := io.Copy(w, countingReader{base: body, bytesRead: &bytesRead})
	cancelReporting()
	<-reportingDone
	return err
}

func (e *localExecutor) Execute(ctx context.Context, namespacedAction *model_executewithstorage.NamespacedAction, executionTimeout time.Duration, executionEvents chan<- *model_core.Decodable[object.LocalReference]) (*model_core.Decodable[object.LocalReference], time.Duration, remoteworker_pb.CurrentState_Completed_Result, error) {
	if !proto.Equal(namespacedAction.Format, actionObjectFormat) {
		return nil, 0, 0, status.Error(codes.InvalidArgument, "This worker cannot execute actions of this type")
	}
	namespace := namespacedAction.Namespace
	referenceFormat := namespace.ReferenceFormat
	actionEncoder, err := e.binaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(
		namespacedAction.Encoders,
		uint32(referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		return nil, 0, 0, util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid action encoders")
	}

	parsedObjectPoolIngester := model_parser.NewParsedObjectPoolIngester(
		e.parsedObjectPool,
		model_parser.NewDownloadingObjectReader(
			object_namespacemapping.NewNamespaceAddingDownloader(e.objectDownloader, namespace.InstanceName),
		),
	)

	var virtualExecutionDuration time.Duration
	result := model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[dag.ObjectContentsWalker]) *model_fetch_pb.Result {
		var result model_fetch_pb.Result
		actionReader := model_parser.LookupParsedObjectReader[object.LocalReference](
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](actionEncoder),
				model_parser.NewProtoObjectParser[object.LocalReference, model_fetch_pb.Action](),
			),
		)
		// An action that is not backed by an object is empty.
		action := model_core.NewSimpleMessage[object.LocalReference](&model_fetch_pb.Action{})
		if namespacedAction.Reference != nil {
			var err error
			action, err = actionReader.ReadObject(ctx, *namespacedAction.Reference)
			if err != nil {
				result.Outcome = &model_fetch_pb.Result_Failure{
					Failure: status.Convert(util.StatusWrap(err, "Failed to read action")).Proto(),
				}
				return &result
			}
		}

		fileCreationParameters, err := model_filesystem.NewFileCreationParametersFromProto(action.Message.FileCreationParameters, referenceFormat, e.binaryEncoderFactory)
		if err != nil {
			result.Outcome = &model_fetch_pb.Result_Failure{
				Failure: status.Convert(util.StatusWrap(err, "Invalid file creation parameters")).Proto(),
			}
			return &result
		}

		target := action.Message.Target
		if target == nil {
			result.Outcome = &model_fetch_pb.Result_Failure{
				Failure: status.New(codes.InvalidArgument, "No target provided").Proto(),
			}
			return &result
		}

		var downloadErrors []error
	ProcessURLs:
		for _, url := range target.Urls {
			downloadedFile, err := e.filePool.NewFile(pool.ZeroHoleSource, 0)
			if err != nil {
				result.Outcome = &model_fetch_pb.Result_Failure{
					Failure: status.Convert(util.StatusWrapWithCode(err, codes.Internal, "Failed to create temporary file for download")).Proto(),
				}
				return &result
			}
			body, sizeBytes, err := e.fetcher.Fetch(ctx, url, target.Headers)
			if err != nil {
				downloadedFile.Close()
				if status.Code(err) != codes.NotFound {
					downloadErrors = append(
						downloadErrors,
						util.StatusWrapfWithCode(err, codes.Internal, "Failed to download file %#v", url),
					)
				}
				continue ProcessURLs
			}
			err = e.copyWithProgress(
				ctx,
				model_filesystem.NewSectionWriter(downloadedFile),
				body,
				url,
				sizeBytes,
				namespace,
				actionEncoder,
				executionEvents,
			)
			body.Close()
			if err != nil {
				downloadedFile.Close()
				downloadErrors = append(
					downloadErrors,
					util.StatusWrapfWithCode(err, codes.Internal, "Failed to download file %#v", url),
				)
				continue ProcessURLs
			}

			var hasher hash.Hash
			if integrity := target.Integrity; integrity == nil {
				hasher = sha256.New()
			} else {
				switch integrity.HashAlgorithm {
				case model_fetch_pb.SubresourceIntegrity_SHA256:
					hasher = sha256.New()
				case model_fetch_pb.SubresourceIntegrity_SHA384:
					hasher = sha512.New384()
				case model_fetch_pb.SubresourceIntegrity_SHA512:
					hasher = sha512.New()
				default:
					downloadedFile.Close()
					result.Outcome = &model_fetch_pb.Result_Failure{
						Failure: status.New(codes.Internal, "Unknown subresource integrity hash algorithm").Proto(),
					}
					return &result
				}
			}
			if _, err := io.Copy(hasher, io.NewSectionReader(downloadedFile, 0, math.MaxInt64)); err != nil {
				downloadedFile.Close()
				result.Outcome = &model_fetch_pb.Result_Failure{
					Failure: status.Convert(util.StatusWrapWithCode(err, codes.Internal, "Failed to hash file")).Proto(),
				}
				return &result
			}
			hash := hasher.Sum(nil)

			var sha256 []byte
			if integrity := target.Integrity; integrity == nil {
				sha256 = hash
			} else if !bytes.Equal(hash, integrity.Hash) {
				downloadedFile.Close()
				result.Outcome = &model_fetch_pb.Result_Failure{
					Failure: status.Newf(codes.InvalidArgument, "File has hash %s, while %s was expected", hex.EncodeToString(hash), hex.EncodeToString(integrity.Hash)).Proto(),
				}
				return &result
			}

			// Compute a Merkle tree of the file and return
			// it. The downloaded file is removed after
			// uploading completes. We don't keep any chunks
			// of data in memory, as we would consume a
			// large amount of memory otherwise.
			fileMerkleTree, err := model_filesystem.CreateChunkDiscardingFileMerkleTree(ctx, fileCreationParameters, downloadedFile)
			if err != nil {
				result.Outcome = &model_fetch_pb.Result_Failure{
					Failure: status.Convert(util.StatusWrapWithCode(err, codes.Internal, "Failed to create file Merkle tree")).Proto(),
				}
				return &result
			}
			patcher.Merge(fileMerkleTree.Patcher)
			result.Outcome = &model_fetch_pb.Result_Success_{
				Success: &model_fetch_pb.Result_Success{
					Contents: fileMerkleTree.Message,
					Sha256:   sha256,
				},
			}
			return &result
		}

		if len(downloadErrors) > 0 {
			result.Outcome = &model_fetch_pb.Result_Failure{
				Failure: status.Convert(util.StatusFromMultiple(downloadErrors)).Proto(),
			}
		} else {
			result.Outcome = &model_fetch_pb.Result_Failure{
				Failure: status.New(codes.NotFound, "File not found").Proto(),
			}
		}
		return &result
	})

	// Store the result in an object.
	resultReference, err := uploadMessage(ctx, e, namespace, actionEncoder, result)
	if err != nil {
		return nil, 0, 0, util.StatusWrap(err, "Failed to store result")
	}

	resultCode := remoteworker_pb.CurrentState_Completed_SUCCEEDED
	if _, ok := result.Message.Outcome.(*model_fetch_pb.Result_Failure); ok {
		resultCode = remoteworker_pb.CurrentState_Completed_FAILED
	}
	return resultReference, virtualExecutionDuration, resultCode, nil
}
