package analysis_test

import (
	"context"
	"encoding"
	"testing"

	model_analysis "bonanza.build/pkg/model/analysis"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/eviction"
	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"go.uber.org/mock/gomock"
)

// baseComputerTester can be used to simulate invocations of build
// analysis functions.
type baseComputerTester struct {
	computer                 model_analysis.Computer[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
	parsedObjectPoolIngester *model_parser.ParsedObjectPoolIngester[model_core.CreatedObjectTree]
	filePool                 *MockFilePool
}

// newBaseComputerTester creates a new instance of BaseComputer that has
// all of its dependencies mocked.
//
// As references to object inputs and metadata of object outputs are of
// type model_core.CreatedObjectTree, the resulting BaseComputer does
// not depend on any storage access.
func newBaseComputerTester(ctrl *gomock.Controller) *baseComputerTester {
	buildSpecificationEncoder := NewMockBinaryEncoder(ctrl)
	buildSpecificationEncoder.EXPECT().GetDecodingParametersSizeBytes().Return(16).AnyTimes()
	executionClient := NewMockExecutionClientForTesting(ctrl)
	filePool := NewMockFilePool(ctrl)
	parsedObjectPoolIngester := model_parser.NewParsedObjectPoolIngester(
		model_parser.NewParsedObjectPool(
			eviction.NewFIFOSet[model_parser.ParsedObjectEvictionKey](),
			/* maximumCount = */ 0,
			/* maximumSizeBytes = */ 0,
		),
		createdObjectReader{},
	)

	bzlFileBuiltins, buildFileBuiltins := model_starlark.GetBuiltins[model_core.CreatedObjectTree, model_core.CreatedObjectTree]()

	return &baseComputerTester{
		computer: model_analysis.NewBaseComputer[model_core.CreatedObjectTree, model_core.CreatedObjectTree](
			parsedObjectPoolIngester,
			object.SHA256V1ReferenceFormat,
			filePool,
			executionClient,
			bzlFileBuiltins,
			buildFileBuiltins,
		),
		parsedObjectPoolIngester: parsedObjectPoolIngester,
		filePool:                 filePool,
	}
}

// expectCaptureCreatedObject can be called by tests to indicate that
// the analysis function may create new objects.
func (bct *baseComputerTester) expectCaptureCreatedObject(e *MockFileRootEnvironmentForTesting) *gomock.Call {
	return e.EXPECT().CaptureCreatedObject(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, createdObject model_core.CreatedObject[model_core.CreatedObjectTree]) (model_core.CreatedObjectTree, error) {
			return model_core.CreatedObjectTree(createdObject), nil
		})
}

// expectCaptureExistingObject can be called by tests to indicate that
// the analysis function may forward objects to other functions.
func (bct *baseComputerTester) expectCaptureExistingObject(e *MockFileRootEnvironmentForTesting) *gomock.Call {
	return e.EXPECT().CaptureExistingObject(gomock.Any()).
		DoAndReturn(func(reference model_core.CreatedObjectTree) model_core.CreatedObjectTree {
			return reference
		})
}

// expectGetDirectoryCreationParametersObjectValue can be called by
// tests to indicate that the analysis function needs access to
// attributes necessary for creating new directories.
func (bct *baseComputerTester) expectGetDirectoryCreationParametersObjectValue(t *testing.T, e *MockFileRootEnvironmentForTesting) *gomock.Call {
	return e.EXPECT().GetDirectoryCreationParametersObjectValue(
		testutil.EqProto(t, &model_analysis_pb.DirectoryCreationParametersObject_Key{}),
	).Return(util.Must(model_filesystem.NewDirectoryCreationParametersFromProto(
		&model_filesystem_pb.DirectoryCreationParameters{
			Access:                    &model_filesystem_pb.DirectoryAccessParameters{},
			DirectoryMaximumSizeBytes: 1 << 16,
		},
		util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
	)), true)
}

// expectGetFileCreationParametersObjectValue can be called by tests to
// indicate that the analysis function needs access to attributes
// necessary for creating new files.
func (bct *baseComputerTester) expectGetFileCreationParametersObjectValue(t *testing.T, e *MockFileRootEnvironmentForTesting) *gomock.Call {
	return e.EXPECT().GetFileCreationParametersObjectValue(
		testutil.EqProto(t, &model_analysis_pb.FileCreationParametersObject_Key{}),
	).Return(util.Must(model_filesystem.NewFileCreationParametersFromProto(
		&model_filesystem_pb.FileCreationParameters{
			Access:                           &model_filesystem_pb.FileAccessParameters{},
			ChunkMinimumSizeBytes:            1 << 16,
			ChunkMaximumSizeBytes:            1 << 18,
			FileContentsListMinimumSizeBytes: 1 << 16,
			FileContentsListMaximumSizeBytes: 1 << 18,
		},
		util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
	)), true)
}

// expectGetFileReaderValue can be called by tests to
// indicate that the analysis function needs access to attributes
// necessary for reading existing files.
func (bct *baseComputerTester) expectGetFileReaderValue(t *testing.T, e *MockFileRootEnvironmentForTesting) *gomock.Call {
	return e.EXPECT().GetFileReaderValue(
		testutil.EqProto(t, &model_analysis_pb.FileReader_Key{}),
	).Return(model_filesystem.NewFileReader(
		model_parser.LookupParsedObjectReader(
			bct.parsedObjectPoolIngester,
			model_filesystem.NewFileContentsListObjectParser[model_core.CreatedObjectTree](),
		),
		model_parser.LookupParsedObjectReader(
			bct.parsedObjectPoolIngester,
			model_parser.NewRawObjectParser[model_core.CreatedObjectTree](),
		),
	), true)
}

// expectGetDirectoryReadersValue can be called by tests to indicate
// that the analysis function needs read access to directories.
func (bct *baseComputerTester) expectGetDirectoryReadersValue(t *testing.T, e *MockFileRootEnvironmentForTesting) *gomock.Call {
	return e.EXPECT().GetDirectoryReadersValue(
		testutil.EqProto(t, &model_analysis_pb.DirectoryReaders_Key{}),
	).Return(&model_analysis.DirectoryReaders[model_core.CreatedObjectTree]{
		DirectoryContents: model_parser.LookupParsedObjectReader(
			bct.parsedObjectPoolIngester,
			model_parser.NewProtoObjectParser[model_core.CreatedObjectTree, model_filesystem_pb.DirectoryContents](),
		),
	}, true)
}

// requireEqualPatchedMessage can be called by tests to validate that an
// analysis function returns the right response.
func requireEqualPatchedMessage[TMessage proto.Message](
	t *testing.T,
	wantBuilder func(*model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) TMessage,
	got model_core.PatchedMessage[TMessage, model_core.CreatedObjectTree],
) {
	t.Helper()
	want := model_core.MustBuildPatchedMessage(wantBuilder)
	if !model_core.PatchedMessagesEqual(want, got) {
		t.Fatalf("Not equal:\nWant:\n\n%s\n\nGot:\n\n%s", protojson.Format(want.Message), protojson.Format(got.Message))
	}
}

// newMessage creates a new message as part of tests.
func newMessage[TMessage any](
	builder func(*model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) TMessage,
) model_core.Message[TMessage, model_core.CreatedObjectTree] {
	m, metadata := model_core.MustBuildPatchedMessage(builder).SortAndSetReferences()
	return model_core.NewMessage(
		m.Message,
		object.OutgoingReferencesList[model_core.CreatedObjectTree](metadata),
	)
}

// newMessage creates a new storage object as part of tests.
func newObject(
	builder func(childPatcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler,
) model_core.Decodable[model_core.CreatedObject[model_core.CreatedObjectTree]] {
	return util.Must(
		model_core.MarshalAndEncode(
			model_core.MustBuildPatchedMessage(builder),
			util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
			model_encoding.NewChainedBinaryEncoder(nil),
		),
	)
}

// attachObject can be called within the builder callback provided to
// newMessage() to attach objects to a message.
func attachObject(
	parentPatcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree],
	createdObject model_core.Decodable[model_core.CreatedObject[model_core.CreatedObjectTree]],
) *model_core_pb.DecodableReference {
	return &model_core_pb.DecodableReference{
		Reference: parentPatcher.AddReference(model_core.MetadataEntry[model_core.CreatedObjectTree]{
			LocalReference: createdObject.Value.GetLocalReference(),
			Metadata:       model_core.CreatedObjectTree(createdObject.Value),
		}),
		DecodingParameters: createdObject.GetDecodingParameters(),
	}
}

// createdObjectReader is used by baseComputerTester to configure
// BaseComputer so that it can read the contents of objects directly out
// of references, as opposed to reading them from storage.
type createdObjectReader struct{}

func (createdObjectReader) ReadParsedObject(ctx context.Context, reference model_core.CreatedObjectTree) (model_core.Message[[]byte, model_core.CreatedObjectTree], error) {
	return model_core.NewMessage(reference.GetPayload(), object.OutgoingReferencesList[model_core.CreatedObjectTree](reference.Metadata)), nil
}

func (createdObjectReader) GetDecodingParametersSizeBytes() int {
	return 0
}

type patchedMessageMatcher[TMessage proto.Message] struct {
	want model_core.PatchedMessage[TMessage, model_core.CreatedObjectTree]
}

var _ gomock.Matcher = patchedMessageMatcher[proto.Message]{}

func eqPatchedMessage[TMessage proto.Message](
	builder func(childPatcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) TMessage,
) gomock.Matcher {
	return patchedMessageMatcher[TMessage]{
		want: model_core.MustBuildPatchedMessage(builder),
	}
}

func (m patchedMessageMatcher[TMessage]) Matches(got any) bool {
	gotMessage, ok := got.(model_core.PatchedMessage[TMessage, model_core.CreatedObjectTree])
	return ok && model_core.PatchedMessagesEqual(m.want, gotMessage)
}

func (m patchedMessageMatcher[TMessage]) String() string {
	return protojson.Format(m.want.Message)
}
