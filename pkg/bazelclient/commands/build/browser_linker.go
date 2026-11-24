package build

import (
	"encoding/base64"
	"net/url"

	browser_pb "bonanza.build/pkg/proto/browser"
	model_encoding_pb "bonanza.build/pkg/proto/model/encoding"

	"google.golang.org/protobuf/proto"
)

// browserLinker creates links to pages of the Bonanza Browser. Each
// link contains the encoders that were used by the build in its
// fragment, so that the browser is able to display the page without
// requiring the user to provide encoder configurations manually.
type browserLinker struct {
	baseURL  string
	fragment string
}

// newBrowserLinker creates a browserLinker that creates links relative
// to a given base URL. If the base URL is empty, nil is returned,
// meaning that no links are created.
func newBrowserLinker(baseURL string, linkedEncoders *browser_pb.LinkedEncoders) (*browserLinker, error) {
	if baseURL == "" {
		return nil, nil
	}
	marshaledLinkedEncoders, err := proto.Marshal(linkedEncoders)
	if err != nil {
		return nil, err
	}
	return &browserLinker{
		baseURL:  baseURL,
		fragment: "#encoders=" + base64.RawURLEncoding.EncodeToString(marshaledLinkedEncoders),
	}, nil
}

// getURL returns the URL of a page of the Bonanza Browser.
func (bl *browserLinker) getURL(pathComponents ...string) (string, bool) {
	if bl == nil {
		return "", false
	}
	pageURL, err := url.JoinPath(bl.baseURL, pathComponents...)
	if err != nil {
		return "", false
	}
	return pageURL + bl.fragment, true
}

// newLinkedEncoder creates an entry of browser_pb.LinkedEncoders,
// containing the encoders used to create objects stored in a given
// field of a message.
func newLinkedEncoder(messageType, fieldName string, configuration []*model_encoding_pb.BinaryEncoder) *browser_pb.RecentlyObservedEncoder {
	return &browser_pb.RecentlyObservedEncoder{
		Configuration: configuration,
		LastObservation: &browser_pb.RecentlyObservedEncoder_LastObservation{
			MessageType: messageType,
			FieldName:   fieldName,
		},
	}
}
