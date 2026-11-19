package evaluation_test

import (
	"testing"

	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"

	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func TestGetValueMessageType(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		messageType, err := evaluation.GetValueMessageType(
			(&model_analysis_pb.RawActionResult_Key{}).ProtoReflect().Descriptor().FullName(),
		)
		require.NoError(t, err)
		require.Equal(t, (&model_analysis_pb.RawActionResult_Value{}).ProtoReflect().Type(), messageType)
	})

	t.Run("NotAKey", func(t *testing.T) {
		_, err := evaluation.GetValueMessageType(
			(&model_analysis_pb.RawActionResult_Value{}).ProtoReflect().Descriptor().FullName(),
		)
		require.EqualError(t, err, "key message type \"bonanza.model.analysis.RawActionResult.Value\" is not named \"Key\"")
	})
}

func TestGetProgressMessageType(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		messageType, err := evaluation.GetProgressMessageType(
			(&model_analysis_pb.RawActionResult_Key{}).ProtoReflect().Descriptor().FullName(),
		)
		require.NoError(t, err)
		require.Equal(t, (&model_analysis_pb.RawActionResult_Progress{}).ProtoReflect().Type(), messageType)
	})

	t.Run("NoProgress", func(t *testing.T) {
		// Not all keys report progress.
		_, err := evaluation.GetProgressMessageType(
			(&model_analysis_pb.ActionEncoders_Key{}).ProtoReflect().Descriptor().FullName(),
		)
		require.ErrorIs(t, err, protoregistry.NotFound)
	})

	t.Run("NotAKey", func(t *testing.T) {
		_, err := evaluation.GetProgressMessageType(protoreflect.FullName("google.protobuf.Empty"))
		require.EqualError(t, err, "key message type \"google.protobuf.Empty\" is not named \"Key\"")
	})
}
