package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"io/fs"
	"math"
	"net/http"
	"time"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	dag_pb "bonanza.build/pkg/proto/storage/dag"
	"bonanza.build/pkg/remoteworker"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"
	object_namespacemapping "bonanza.build/pkg/storage/object/namespacemapping"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/pool"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type localExecutor struct {
	objectDownloader              object.Downloader[object.GlobalReference]
	parsedObjectPool              *model_parser.ParsedObjectPool
	dagUploaderClient             dag_pb.UploaderClient
	objectContentsWalkerSemaphore *semaphore.Weighted
	httpClient                    *http.Client
	filePool                      pool.FilePool
	cacheDirectory                filesystem.Directory
}

// NewLocalExecutor creates a remote worker protocol executor that is
// capable of processing "fetch" actions. URLs listed in the action are
// fetched using a HTTP client, and the resulting file is uploaded to
// storage.
func NewLocalExecutor(
	objectDownloader object.Downloader[object.GlobalReference],
	parsedObjectPool *model_parser.ParsedObjectPool,
	dagUploaderClient dag_pb.UploaderClient,
	objectContentsWalkerSemaphore *semaphore.Weighted,
	httpClient *http.Client,
	filePool pool.FilePool,
	cacheDirectory filesystem.Directory,
) remoteworker.Executor[*model_executewithstorage.Action[object.GlobalReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]] {
	return &localExecutor{
		objectDownloader:              objectDownloader,
		parsedObjectPool:              parsedObjectPool,
		dagUploaderClient:             dagUploaderClient,
		objectContentsWalkerSemaphore: objectContentsWalkerSemaphore,
		httpClient:                    httpClient,
		filePool:                      filePool,
		cacheDirectory:                cacheDirectory,
	}
}

func (e *localExecutor) CheckReadiness(ctx context.Context) error {
	return nil
}

func (e *localExecutor) Execute(ctx context.Context, action *model_executewithstorage.Action[object.GlobalReference], executionTimeout time.Duration, executionEvents chan<- model_core.Decodable[object.LocalReference]) (model_core.Decodable[object.LocalReference], time.Duration, remoteworker_pb.CurrentState_Completed_Result, error) {
	if !proto.Equal(action.Format, &model_core_pb.ObjectFormat{
		Format: &model_core_pb.ObjectFormat_ProtoTypeName{
			ProtoTypeName: "bonanza.model.fetch.Action",
		},
	}) {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, status.Error(codes.InvalidArgument, "This worker cannot execute actions of this type")
	}
	referenceFormat := action.Reference.Value.GetReferenceFormat()
	actionEncoder, err := model_encoding.NewBinaryEncoderFromProto(
		action.Encoders,
		uint32(referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, status.Error(codes.InvalidArgument, "Invalid action encoders")
	}

	parsedObjectPoolIngester := model_parser.NewParsedObjectPoolIngester(
		e.parsedObjectPool,
		model_parser.NewDownloadingParsedObjectReader(
			object_namespacemapping.NewNamespaceAddingDownloader(e.objectDownloader, action.Reference.Value.InstanceName),
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
		action, err := actionReader.ReadParsedObject(ctx, model_core.CopyDecodable(action.Reference, action.Reference.Value.GetLocalReference()))
		if err != nil {
			result.Outcome = &model_fetch_pb.Result_Failure{
				Failure: status.Convert(util.StatusWrap(err, "Failed to read action")).Proto(),
			}
			return &result
		}

		fileCreationParameters, err := model_filesystem.NewFileCreationParametersFromProto(action.Message.FileCreationParameters, referenceFormat)
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
			// Store copies of the file in a local cache directory.
			// TODO: Remove this feature once our storage is robust enough.
			urlHash := sha256.Sum256([]byte(url))
			filename := path.MustNewComponent(hex.EncodeToString(urlHash[:]))
			downloadedFile, err := e.cacheDirectory.OpenReadWrite(filename, filesystem.DontCreate)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					result.Outcome = &model_fetch_pb.Result_Failure{
						Failure: status.Convert(util.StatusWrapWithCode(err, codes.Internal, "Failed to open file in cache directory")).Proto(),
					}
					return &result
				}
				downloadedFile, err = e.cacheDirectory.OpenReadWrite(filename, filesystem.CreateExcl(0o666))
				if err != nil {
					result.Outcome = &model_fetch_pb.Result_Failure{
						Failure: status.Convert(util.StatusWrapWithCode(err, codes.Internal, "Failed to create file in cache directory")).Proto(),
					}
					return &result
				}

				req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				if err != nil {
					downloadedFile.Close()
					result.Outcome = &model_fetch_pb.Result_Failure{
						Failure: status.Convert(util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to create HTTP request")).Proto(),
					}
					return &result
				}
				for _, entry := range target.Headers {
					req.Header.Set(entry.Name, entry.Value)
				}

				resp, err := e.httpClient.Do(req)
				if err != nil {
					downloadedFile.Close()
					result.Outcome = &model_fetch_pb.Result_Failure{
						Failure: status.Convert(util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to perform HTTP request")).Proto(),
					}
					downloadErrors = append(
						downloadErrors,
						util.StatusWrapfWithCode(err, codes.Internal, "Failed to download file file %#v", url),
					)
					continue ProcessURLs
				}
				defer resp.Body.Close()

				switch resp.StatusCode {
				case http.StatusOK:
					// Download the file to the local system.
					if _, err := io.Copy(model_filesystem.NewSectionWriter(downloadedFile), resp.Body); err != nil {
						downloadedFile.Close()
						e.cacheDirectory.Remove(filename)
						downloadErrors = append(
							downloadErrors,
							util.StatusWrapfWithCode(err, codes.Internal, "Failed to download file %#v", url),
						)
						continue ProcessURLs
					}
				case http.StatusNotFound:
					downloadedFile.Close()
					e.cacheDirectory.Remove(filename)
					continue ProcessURLs
				default:
					downloadedFile.Close()
					e.cacheDirectory.Remove(filename)
					downloadErrors = append(
						downloadErrors,
						status.Errorf(codes.Internal, "Received unexpected HTTP response %#v when downloading file %#v", resp.Status, url),
					)
					continue ProcessURLs
				}
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
					e.cacheDirectory.Remove(filename)
					result.Outcome = &model_fetch_pb.Result_Failure{
						Failure: status.New(codes.Internal, "Unknown subresource integrity hash algorithm").Proto(),
					}
					return &result
				}
			}
			if _, err := io.Copy(hasher, io.NewSectionReader(downloadedFile, 0, math.MaxInt64)); err != nil {
				downloadedFile.Close()
				e.cacheDirectory.Remove(filename)
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
				e.cacheDirectory.Remove(filename)
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

	createdResult, err := model_core.MarshalAndEncode(
		model_core.ProtoToBinaryMarshaler(result),
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
		action.Reference.Value.WithLocalReference(resultReference),
		dag.NewSimpleObjectContentsWalker(
			createdResult.Value.Contents,
			createdResult.Value.Metadata,
		),
		e.objectContentsWalkerSemaphore,
		// Assume everything we attempt to upload is memory backed.
		object.Unlimited,
	); err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, util.StatusWrap(err, "Failed to upload result")
	}

	resultCode := remoteworker_pb.CurrentState_Completed_SUCCEEDED
	if _, ok := result.Message.Outcome.(*model_fetch_pb.Result_Failure); ok {
		resultCode = remoteworker_pb.CurrentState_Completed_FAILED
	}
	return model_core.CopyDecodable(createdResult, resultReference), virtualExecutionDuration, resultCode, nil
}
