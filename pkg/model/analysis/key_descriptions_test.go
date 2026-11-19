package analysis_test

import (
	"testing"

	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"

	"github.com/stretchr/testify/require"
)

// TestKeyDescriptions ensures that all keys used by the analysis
// computer have a valid human readable description, so that tools such
// as bonanza_bazel and bonanza_browser can display them.
func TestKeyDescriptions(t *testing.T) {
	messages := model_analysis_pb.File_bonanza_build_pkg_proto_model_analysis_analysis_proto.Messages()
	keysCount := 0
	for i := range messages.Len() {
		key := messages.Get(i).Messages().ByName("Key")
		if key == nil {
			continue
		}
		keysCount++
		t.Run(string(messages.Get(i).Name()), func(t *testing.T) {
			ok, err := model_evaluation.ValidateKeyDescription(key)
			require.NoError(t, err)
			require.True(t, ok, "Key has no bonanza.model.evaluation.key_description option")
		})
	}
	require.NotZero(t, keysCount)
}
