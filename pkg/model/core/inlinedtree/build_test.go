package inlinedtree_test

import (
	"encoding"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_format "bonanza.build/pkg/model/format"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"

	"go.uber.org/mock/gomock"
)

func TestBuild(t *testing.T) {
	ctrl := gomock.NewController(t)

	t.Run("NoCandidates", func(t *testing.T) {
		// If no candidates are provided, there is no data,
		// meaning an empty message needs to be emitted.
		output, err := inlinedtree.Build(
			inlinedtree.CandidateList[*model_command_pb.PathPattern, model_core.ReferenceMetadata]{},
			inlinedtree.Options{
				ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				MaximumSizeBytes: 16 * 1024,
			},
		)
		require.NoError(t, err)

		references, metadata := output.Patcher.SortAndSetReferences()
		testutil.RequireEqualProto(t, &model_command_pb.PathPattern{}, output.Message)
		require.Empty(t, references)
		require.Empty(t, metadata)
	})

	t.Run("SingleCandidateInlineTiny", func(t *testing.T) {
		// If the candidate is so small that it takes less space
		// to encode than an actual reference, it must be
		// inlined, even if the maximum output size does not
		// permit it.
		encoder := NewMockDeterministicBinaryEncoder(ctrl)
		encoder.EXPECT().GetDecodingParametersSizeBytes().Return(4)

		leaves := &model_command_pb.PathPattern_Children{
			Children: []*model_command_pb.PathPattern_Child{{
				Name:    "a",
				Pattern: &model_command_pb.PathPattern{},
			}},
		}
		parentAppender := NewMockParentAppenderForTesting(ctrl)
		metadata1 := NewMockReferenceMetadata(ctrl)
		parentAppender.EXPECT().Call(gomock.Any(), nil).
			Do(func(output model_core.PatchedMessage[*model_command_pb.PathPattern, model_core.ReferenceMetadata], externalObject *model_core.Decodable[model_core.CreatedObject[model_core.ReferenceMetadata]]) {
				output.Message.Children = &model_command_pb.PathPattern_ChildrenInline{ChildrenInline: leaves}
			}).
			Times(2)
		parentAppender.EXPECT().Call(gomock.Any(), gomock.Not(nil)).
			Do(func(output model_core.PatchedMessage[*model_command_pb.PathPattern, model_core.ReferenceMetadata], externalObject *model_core.Decodable[model_core.CreatedObject[model_core.ReferenceMetadata]]) {
				output.Message.Children = &model_command_pb.PathPattern_ChildrenExternal{ChildrenExternal: model_core_pb.DecodableReference_builder{
					Reference: output.Patcher.AddReference(model_core.MetadataEntry[model_core.ReferenceMetadata]{
						LocalReference: externalObject.Value.GetLocalReference(),
						Metadata:       metadata1,
					}),
					DecodingParameters: externalObject.GetDecodingParameters(),
				}.Build()}
			}).
			Times(1)
		metadata1.EXPECT().Discard()

		output, err := inlinedtree.Build(
			inlinedtree.CandidateList[*model_command_pb.PathPattern, model_core.ReferenceMetadata]{{
				ExternalMessage: model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata](model_format.NewProto[model_command_pb.PathPattern_Children]().NewMarshaler(leaves)),
				Encoder:         encoder,
				ParentAppender:  parentAppender.Call,
			}},
			inlinedtree.Options{
				ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				MaximumSizeBytes: 0,
			},
		)
		require.NoError(t, err)

		references, metadata := output.Patcher.SortAndSetReferences()
		testutil.RequireEqualProto(t, &model_command_pb.PathPattern{
			Children: &model_command_pb.PathPattern_ChildrenInline{ChildrenInline: leaves},
		}, output.Message)
		require.Empty(t, references)
		require.Empty(t, metadata)
	})

	t.Run("SingleCandidateExternal", func(t *testing.T) {
		// If there is no space left and storing a reference
		// takes up less space than inlining the data, we should
		// emit a reference.
		encoder := NewMockDeterministicBinaryEncoder(ctrl)
		encoder.EXPECT().GetDecodingParametersSizeBytes().Return(4)
		encoder.EXPECT().EncodeBinary(gomock.Any()).
			DoAndReturn(func(in []byte) ([]byte, []byte, error) {
				return in, []byte{1, 2, 3, 4}, nil
			})

		leaves := &model_command_pb.PathPattern_Children{
			Children: []*model_command_pb.PathPattern_Child{
				{Name: "This is a very long path pattern child name", Pattern: &model_command_pb.PathPattern{}},
				{Name: "This is another long path pattern child name", Pattern: &model_command_pb.PathPattern{}},
			},
		}
		parentAppender := NewMockParentAppenderForTesting(ctrl)
		parentAppender.EXPECT().Call(gomock.Any(), nil).
			Do(func(output model_core.PatchedMessage[*model_command_pb.PathPattern, model_core.ReferenceMetadata], externalObject *model_core.Decodable[model_core.CreatedObject[model_core.ReferenceMetadata]]) {
				output.Message.Children = &model_command_pb.PathPattern_ChildrenInline{ChildrenInline: proto.ValueOrDefault(leaves)}
			}).
			Times(1)
		metadata1 := NewMockReferenceMetadata(ctrl)
		parentAppender.EXPECT().Call(gomock.Any(), gomock.Not(nil)).
			Do(func(output model_core.PatchedMessage[*model_command_pb.PathPattern, model_core.ReferenceMetadata], externalObject *model_core.Decodable[model_core.CreatedObject[model_core.ReferenceMetadata]]) {
				output.Message.Children = &model_command_pb.PathPattern_ChildrenExternal{ChildrenExternal: model_core_pb.DecodableReference_builder{
					Reference: output.Patcher.AddReference(model_core.MetadataEntry[model_core.ReferenceMetadata]{
						LocalReference: externalObject.Value.GetLocalReference(),
						Metadata:       metadata1,
					}),
					DecodingParameters: externalObject.GetDecodingParameters(),
				}.Build()}
			}).
			Times(2)
		metadata1.EXPECT().Discard().Times(1)

		output, err := inlinedtree.Build(
			inlinedtree.CandidateList[*model_command_pb.PathPattern, model_core.ReferenceMetadata]{{
				ExternalMessage: model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata](model_format.NewProto[model_command_pb.PathPattern_Children]().NewMarshaler(leaves)),
				Encoder:         encoder,
				ParentAppender:  parentAppender.Call,
			}},
			inlinedtree.Options{
				ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				MaximumSizeBytes: 0,
			},
		)
		require.NoError(t, err)

		references, metadata := output.Patcher.SortAndSetReferences()
		testutil.RequireEqualProto(t, &model_command_pb.PathPattern{
			Children: &model_command_pb.PathPattern_ChildrenExternal{ChildrenExternal: model_core_pb.DecodableReference_builder{
				Reference: model_core_pb.Reference_builder{
					Index: 1,
				}.Build(),
				DecodingParameters: []byte{1, 2, 3, 4},
			}.Build()},
		}, output.Message)
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
			object.MustNewSHA256V1LocalReference("a7293026e8db38c82bc46e2a6c033b5bbaafc3c17e9bbc9737bcf313997dfc8a", 99, 0, 0, 0),
		}, references)
		require.Equal(t, []model_core.ReferenceMetadata{
			metadata1,
		}, metadata)
	})

	t.Run("SingleCandidateInline", func(t *testing.T) {
		// If the maximum message size if sufficiently large to
		// be able to inline the candidate, it should not store
		// the data externally.
		encoder := NewMockDeterministicBinaryEncoder(ctrl)

		leaves := &model_command_pb.PathPattern_Children{
			Children: []*model_command_pb.PathPattern_Child{
				{Name: "This is a very long path pattern child name", Pattern: &model_command_pb.PathPattern{}},
				{Name: "This is another long path pattern child name", Pattern: &model_command_pb.PathPattern{}},
			},
		}
		parentAppender := NewMockParentAppenderForTesting(ctrl)
		parentAppender.EXPECT().Call(gomock.Any(), nil).
			Do(func(output model_core.PatchedMessage[*model_command_pb.PathPattern, model_core.ReferenceMetadata], externalObject *model_core.Decodable[model_core.CreatedObject[model_core.ReferenceMetadata]]) {
				output.Message.Children = &model_command_pb.PathPattern_ChildrenInline{ChildrenInline: leaves}
			}).
			Times(2)

		output, err := inlinedtree.Build(
			inlinedtree.CandidateList[*model_command_pb.PathPattern, model_core.ReferenceMetadata]{{
				ExternalMessage: model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata](model_format.NewProto[model_command_pb.PathPattern_Children]().NewMarshaler(leaves)),
				Encoder:         encoder,
				ParentAppender:  parentAppender.Call,
			}},
			inlinedtree.Options{
				ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				MaximumSizeBytes: 200,
			},
		)
		require.NoError(t, err)

		references, metadata := output.Patcher.SortAndSetReferences()
		testutil.RequireEqualProto(t, &model_command_pb.PathPattern{
			Children: &model_command_pb.PathPattern_ChildrenInline{ChildrenInline: leaves},
		}, output.Message)
		require.Empty(t, references)
		require.Empty(t, metadata)
	})

	t.Run("SingleCandidateForceInline", func(t *testing.T) {
		// If ExternalMessage.Message is not set, the caller
		// forces inlining to be performed. In this case there
		// should be no need to invoke the ParentAppender to
		// compute the size of the externally stored instance.
		leaves := &model_command_pb.PathPattern_Children{
			Children: []*model_command_pb.PathPattern_Child{
				{Name: "This is a very long path pattern child name", Pattern: &model_command_pb.PathPattern{}},
				{Name: "This is another long path pattern child name", Pattern: &model_command_pb.PathPattern{}},
			},
		}
		parentAppender := NewMockParentAppenderForTesting(ctrl)
		parentAppender.EXPECT().Call(gomock.Any(), nil).
			Do(func(output model_core.PatchedMessage[*model_command_pb.PathPattern, model_core.ReferenceMetadata], externalObject *model_core.Decodable[model_core.CreatedObject[model_core.ReferenceMetadata]]) {
				output.Message.Children = &model_command_pb.PathPattern_ChildrenInline{ChildrenInline: leaves}
			}).
			Times(2)

		output, err := inlinedtree.Build(
			inlinedtree.CandidateList[*model_command_pb.PathPattern, model_core.ReferenceMetadata]{{
				ExternalMessage: model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata](encoding.BinaryMarshaler(nil)),
				ParentAppender:  parentAppender.Call,
			}},
			inlinedtree.Options{
				ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				MaximumSizeBytes: 200,
			},
		)
		require.NoError(t, err)

		references, metadata := output.Patcher.SortAndSetReferences()
		testutil.RequireEqualProto(t, &model_command_pb.PathPattern{
			Children: &model_command_pb.PathPattern_ChildrenInline{ChildrenInline: leaves},
		}, output.Message)
		require.Empty(t, references)
		require.Empty(t, metadata)
	})
}
