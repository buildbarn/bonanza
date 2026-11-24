package build

import (
	"encoding/base64"
	"strings"
	"testing"

	browser_pb "bonanza.build/pkg/proto/browser"
	model_encoding_pb "bonanza.build/pkg/proto/model/encoding"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"
)

func TestBrowserLinker(t *testing.T) {
	t.Run("NoBaseURL", func(t *testing.T) {
		// If no URL of the Bonanza Browser is provided, no
		// links should be created.
		browser, err := newBrowserLinker("", &browser_pb.LinkedEncoders{})
		require.NoError(t, err)
		_, ok := browser.getURL("object", "foo")
		require.False(t, ok)
	})

	t.Run("Success", func(t *testing.T) {
		linkedEncoders := &browser_pb.LinkedEncoders{
			Encoders: []*browser_pb.RecentlyObservedEncoder{
				newLinkedEncoder("DirectoryAccessParameters", "encoders", []*model_encoding_pb.BinaryEncoder{{
					Encoder: &model_encoding_pb.BinaryEncoder_Encrypting{
						Encrypting: &model_encoding_pb.EncryptingBinaryEncoder{
							EncryptionKey: []byte("0123456789abcdef"),
						},
					},
				}}),
			},
		}
		browser, err := newBrowserLinker("https://browser.example.com/", linkedEncoders)
		require.NoError(t, err)

		// Encoders should be stored in the fragment, so that
		// they are not sent to the server.
		pageURL, ok := browser.getURL("evaluation", "my instance", "SHA256_V1")
		require.True(t, ok)
		page, fragment, ok := strings.Cut(pageURL, "#encoders=")
		require.True(t, ok)
		require.Equal(t, "https://browser.example.com/evaluation/my%20instance/SHA256_V1", page)

		marshaled, err := base64.RawURLEncoding.DecodeString(fragment)
		require.NoError(t, err)
		var unmarshaled browser_pb.LinkedEncoders
		require.NoError(t, proto.Unmarshal(marshaled, &unmarshaled))
		testutil.RequireEqualProto(t, linkedEncoders, &unmarshaled)
	})
}
