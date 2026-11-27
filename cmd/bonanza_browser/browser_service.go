package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bonanza.build/pkg/crypto"
	"bonanza.build/pkg/encoding/varint"
	"bonanza.build/pkg/encryptedaction"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_format "bonanza.build/pkg/model/format"
	model_parser "bonanza.build/pkg/model/parser"
	browser_pb "bonanza.build/pkg/proto/browser"
	buildqueuestate_pb "bonanza.build/pkg/proto/buildqueuestate"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_encoding_pb "bonanza.build/pkg/proto/model/encoding"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	model_executewithstorage_pb "bonanza.build/pkg/proto/model/executewithstorage"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	object_namespacemapping "bonanza.build/pkg/storage/object/namespacemapping"

	http_server "github.com/buildbarn/bb-storage/pkg/http/server"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	g "maragu.dev/gomponents"
	c "maragu.dev/gomponents/components"
	h "maragu.dev/gomponents/html"
)

//go:embed stylesheet.css
var stylesheet string

// BrowserService is capable of serving pages for inspecting the
// contents of objects in storage.
type BrowserService struct {
	buildQueueStateClient buildqueuestate_pb.BuildQueueStateClient
	objectDownloader      object.Downloader[object.GlobalReference]
	parsedObjectPool      *model_parser.ParsedObjectPool
}

// NewBrowserService creates a new BrowserService that serves pages,
// displaying the contents contained in a given storage backend.
func NewBrowserService(buildQueueStateClient buildqueuestate_pb.BuildQueueStateClient, objectDownloader object.Downloader[object.GlobalReference], parsedObjectPool *model_parser.ParsedObjectPool) *BrowserService {
	return &BrowserService{
		buildQueueStateClient: buildQueueStateClient,
		objectDownloader:      objectDownloader,
		parsedObjectPool:      parsedObjectPool,
	}
}

// validateEncoderConfiguration returns an error if an encoder
// configuration cannot be used to decode objects (e.g., because it
// contains an encryption key of an unsupported size). This prevents
// such configurations from being stored in the cookie.
func validateEncoderConfiguration(configuration []*model_encoding_pb.BinaryEncoder) error {
	// The maximum decoded size only limits the size of data that
	// is decoded. It does not affect whether the configuration is
	// valid.
	_, err := browserBinaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(configuration, 1)
	return err
}

// importLinkedEncoders processes encoders that were embedded in the
// fragment of a link to the current page. These are submitted by the
// script contained in each page (see linkedEncodersScript). The
// encoders are stored in the cookie, after which the client is
// redirected to the same page, which can then be displayed using
// these encoders.
//
// This function returns true if the request contained linked encoders,
// meaning a response has already been written.
func importLinkedEncoders(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	value := r.PostFormValue("linked_encoders")
	if value == "" {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		http.Error(w, "failed to decode linked encoders: "+err.Error(), http.StatusBadRequest)
		return true
	}
	var linkedEncoders browser_pb.LinkedEncoders
	if err := proto.Unmarshal(data, &linkedEncoders); err != nil {
		http.Error(w, "failed to unmarshal linked encoders: "+err.Error(), http.StatusBadRequest)
		return true
	}

	now := timestamppb.Now()
	for i, encoder := range linkedEncoders.GetEncoders() {
		if err := validateEncoderConfiguration(encoder.GetConfiguration()); err != nil {
			http.Error(w, fmt.Sprintf("invalid linked encoder at index %d: %s", i, err), http.StatusBadRequest)
			return true
		}
		if o := encoder.GetLastObservation(); o != nil {
			o.SetTime(now)
		}
	}
	cookie := getCookie(r)
	cookie.SetRecentlyObservedEncoders(trimRecentlyObservedEncoders(
		append(linkedEncoders.GetEncoders(), cookie.GetRecentlyObservedEncoders()...),
	))
	setCookie(w, cookie)
	http.Redirect(w, r, r.URL.RequestURI(), http.StatusSeeOther)
	return true
}

// importEncoderConfiguration processes an encoder configuration that
// was submitted through a form, either by entering a new configuration
// or by selecting a recently observed one. The configuration is stored
// in the cookie, after which the client is redirected to the same page.
// This prevents the browser from asking to resubmit the form when the
// page is reloaded.
//
// Invalid configurations are rejected by displaying an error page.
//
// This function returns true if the request contained a
// configuration, meaning a response has already been written.
func importEncoderConfiguration(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	value := r.PostFormValue("encoder_configuration")
	if value == "" {
		return false
	}
	var configuration protoList[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder]
	if err := json.Unmarshal([]byte(value), &configuration); err != nil {
		http.Error(w, "failed to unmarshal encoder configuration: "+err.Error(), http.StatusBadRequest)
		return true
	}
	if err := validateEncoderConfiguration(configuration); err != nil {
		http.Error(w, "invalid encoder configuration: "+err.Error(), http.StatusBadRequest)
		return true
	}
	cookie := getCookie(r)
	cookie.SetRecentlyObservedEncoders(trimRecentlyObservedEncoders(
		append(
			[]*browser_pb.RecentlyObservedEncoder{browser_pb.RecentlyObservedEncoder_builder{
				Configuration: configuration,
			}.Build()},
			cookie.GetRecentlyObservedEncoders()...,
		),
	))
	setCookie(w, cookie)
	http.Redirect(w, r, r.URL.RequestURI(), http.StatusSeeOther)
	return true
}

func wrapHandler(handler func(http.ResponseWriter, *http.Request) (g.Node, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if importLinkedEncoders(w, r) || importEncoderConfiguration(w, r) {
			return
		}
		node, err := handler(w, r)
		if err != nil {
			st := status.Convert(err)
			http.Error(w, err.Error(), http_server.StatusCodeFromGRPCCode(st.Code()))
			return
		}

		// The handler may return a nil node to indicate that it
		// is responsible for writing its own output (e.g.,
		// performing a HTTP redirect).
		if node != nil {
			if err := node.Render(w); err != nil {
				http.Error(w, "error rendering node: "+err.Error(), http.StatusInternalServerError)
			}
		}
	}
}

// RegisterHandlers registers handlers for serving web pages in a mux.
func (s *BrowserService) RegisterHandlers(mux *http.ServeMux) {
	// Storage related endpoints.
	mux.HandleFunc(
		"/evaluation/{instance_name}/{reference_format}/{reference}/{key_path...}",
		wrapHandler(s.doEvaluation),
	)
	mux.HandleFunc(
		"/object/{instance_name}/{reference_format}/{reference}/proto/{message_type}",
		wrapHandler(s.doProtoObject),
	)
	mux.HandleFunc(
		"/object/{instance_name}/{reference_format}/{reference}/proto_list/{message_type}",
		wrapHandler(s.doProtoListObject),
	)
	mux.HandleFunc(
		"/object/{instance_name}/{reference_format}/{reference}/raw",
		wrapHandler(s.doRawObject),
	)

	// Scheduler related endpoints.
	mux.HandleFunc("/{$}", wrapHandler(s.doPlatformQueues))
	mux.HandleFunc("/operation/{operation_name}", wrapHandler(s.doOperation))
	mux.HandleFunc("/operations/{filter_stage}", wrapHandler(s.doOperations))
	mux.HandleFunc("/workers/{filter}", wrapHandler(s.doWorkers))
}

// linkedEncodersScript is embedded into each page. If the page was
// opened through a link containing encoders in its fragment (see
// browser_pb.LinkedEncoders), it removes the fragment from the
// location and the browser's history, and submits the encoders to the
// server. Fragments are used, as they are not sent to the server as
// part of HTTP requests. This prevents secrets contained in the
// encoders from ending up in access logs.
const linkedEncodersScript = `(() => {
  const prefix = "#encoders=";
  if (!location.hash.startsWith(prefix)) {
    return;
  }
  const linkedEncoders = location.hash.slice(prefix.length);
  const target = location.pathname + location.search;
  history.replaceState(null, "", target);
  const form = document.createElement("form");
  form.method = "post";
  form.action = target;
  const input = document.createElement("input");
  input.type = "hidden";
  input.name = "linked_encoders";
  input.value = linkedEncoders;
  form.append(input);
  document.documentElement.append(form);
  form.submit();
})();`

func renderPage(title string, body []g.Node) g.Node {
	return c.HTML5(c.HTML5Props{
		Title:    title,
		Language: "en",
		Head: []g.Node{
			h.StyleEl(g.Raw(stylesheet)),
			h.Script(g.Raw(linkedEncodersScript)),
		},
		Body: append(
			[]g.Node{
				h.Div(
					h.Class("navbar bg-primary text-primary-content"),
					h.A(
						h.Class("btn btn-ghost text-2xl"),
						h.Href("/"),
						g.Text("Bonanza Browser"),
					),
				),
			},
			body...,
		),
	})
}

// getReferenceFromRequest extracts the instance name, reference format,
// and reference fields embedded in a request's URL and converts them to
// an object.GlobalReference that can be used to download an object.
func getReferenceFromRequest(r *http.Request) (model_core.Decodable[object.GlobalReference], error) {
	var bad model_core.Decodable[object.GlobalReference]
	instanceNameStr := r.PathValue("instance_name")
	if instanceNameStr == "-" {
		instanceNameStr = ""
	}
	instanceName, err := object.NewInstanceName(instanceNameStr)
	if err != nil {
		return bad, util.StatusWrapf(err, "Invalid instance name %#v", instanceNameStr)
	}

	referenceFormatStr := r.PathValue("reference_format")
	referenceFormatValue, ok := object_pb.ReferenceFormat_Value_value[referenceFormatStr]
	if !ok {
		return bad, status.Errorf(codes.InvalidArgument, "Invalid reference format %#v", referenceFormatStr)
	}
	referenceFormat, err := object.NewReferenceFormat(object_pb.ReferenceFormat_Value(referenceFormatValue))
	if err != nil {
		return bad, util.StatusWrapf(err, "Invalid reference format %#v", referenceFormatStr)
	}

	localReference, err := model_core.NewDecodableLocalReferenceFromString(
		referenceFormat,
		r.PathValue("reference"),
	)
	if err != nil {
		return bad, util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid reference")
	}

	return model_core.CopyDecodable(
		localReference,
		instanceName.WithLocalReference(localReference.Value),
	), nil
}

// trimRecentlyObservedEncoders takes a list of recently observed object
// encoders and removes all encoders that have the same configuration.
// It also limits the maximum number of entries to a small number, so
// that the page doesn't become too cluttered.
func trimRecentlyObservedEncoders(in []*browser_pb.RecentlyObservedEncoder) []*browser_pb.RecentlyObservedEncoder {
	var out []*browser_pb.RecentlyObservedEncoder
	seen := map[string]int{}
	for _, encoder := range in {
		if marshaled, err := proto.Marshal(
			browser_pb.RecentlyObservedEncoder_builder{
				Configuration: encoder.GetConfiguration(),
			}.Build(),
		); err == nil {
			key := string(marshaled)
			if index, ok := seen[key]; ok {
				if existing := out[index]; encoder.GetLastObservation().GetTime().AsTime().
					After(existing.GetLastObservation().GetTime().AsTime()) {
					existing.SetLastObservation(encoder.GetLastObservation())
				}
			} else {
				seen[key] = len(out)
				out = append(out, encoder)
			}
		}
	}
	if maximumCount := 10; len(out) > maximumCount {
		out = out[:maximumCount]
	}
	return out
}

func getCookie(r *http.Request) *browser_pb.Cookie {
	cookie, err := r.Cookie("bonanza_browser")
	if err != nil {
		return &browser_pb.Cookie{}
	}
	cookieBytes, err := base64.RawURLEncoding.AppendDecode(nil, []byte(cookie.Value))
	if err != nil {
		return &browser_pb.Cookie{}
	}
	var cookieMessage browser_pb.Cookie
	if err := proto.Unmarshal(cookieBytes, &cookieMessage); err != nil {
		return &browser_pb.Cookie{}
	}
	return &cookieMessage
}

func setCookie(w http.ResponseWriter, cookieMessage *browser_pb.Cookie) {
	if cookieBytes, err := proto.Marshal(cookieMessage); err == nil {
		http.SetCookie(w, &http.Cookie{
			Name:     "bonanza_browser",
			Value:    base64.RawURLEncoding.EncodeToString(cookieBytes),
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
	}
}

// getEncodersFromRequest returns the cookie containing the list of
// recently observed encoders. An empty encoder is always appended to
// the list, guaranteeing that it is non-empty.
func getEncodersFromRequest(r *http.Request) *browser_pb.Cookie {
	cookie := getCookie(r)
	cookie.SetRecentlyObservedEncoders(trimRecentlyObservedEncoders(
		append(cookie.GetRecentlyObservedEncoders(), &browser_pb.RecentlyObservedEncoder{}),
	))
	return cookie
}

// renderTabsLiftWithNeutralContent renders a set of tabs with the
// contents of the selected tab below them. The selected tab has a dark
// background color.
func renderTabsLiftWithNeutralContent(tabs [][]g.Node, selectedTabIndex int, content []g.Node) g.Node {
	nodes := append(
		make([]g.Node, len(tabs)+2),
		h.Class("tabs tabs-lift"),
	)
	for i, tab := range tabs {
		if i == selectedTabIndex {
			nodes = append(
				nodes,
				h.A(
					append(
						[]g.Node{h.Class("tab tab-active [--tab-bg:var(--color-neutral)] text-neutral-content!")},
						tab...,
					)...,
				),
				h.Div(
					append(
						[]g.Node{
							h.Class("tab-content p-4 border-base-300 bg-neutral text-neutral-content font-mono h-auto! overflow-x-auto"),
						},
						content...,
					)...,
				),
			)
		} else {
			nodes = append(
				nodes,
				h.A(
					append(
						[]g.Node{h.Class("tab")},
						tab...,
					)...,
				),
			)
		}
	}
	return h.Div(nodes...)
}

// renderReferenceCard renders a card containing all of the properties
// of an object reference. This card is shown on the top left of both
// the evaluation and object pages.
func renderReferenceCard(title string, decodableReference model_core.Decodable[object.GlobalReference]) g.Node {
	return h.Div(
		h.Class("card bg-base-200 p-4 shadow"),
		h.H1(
			h.Class("card-title text-2xl mb-4"),
			g.Text(title),
		),
		h.Table(
			h.Class("table"),

			h.Tr(
				h.Th(
					h.Class("whitespace-nowrap"),
					g.Text("Object:"),
				),
				h.Td(
					h.Class("break-all"),
					h.Span(
						h.Class("font-mono"),
						g.Text(base64.RawURLEncoding.EncodeToString(decodableReference.Value.GetRawReference())),
					),
				),
			),
			h.Tr(
				h.Th(
					h.Class("whitespace-nowrap"),
					g.Text("Decoding parameters:"),
				),
				h.Td(
					h.Class("break-all"),
					h.Span(
						h.Class("font-mono"),
						g.Text(base64.RawURLEncoding.EncodeToString(decodableReference.GetDecodingParameters())),
					),
				),
			),
			h.Tr(
				h.Th(
					h.Class("whitespace-nowrap"),
					g.Text("SHA-256 hash:"),
				),
				h.Td(
					h.Class("break-all"),
					h.Span(
						h.Class("font-mono"),
						g.Text(hex.EncodeToString(decodableReference.Value.GetHash())),
					),
				),
			),
			h.Tr(
				h.Th(
					h.Class("whitespace-nowrap"),
					g.Text("Size:"),
				),
				h.Td(
					g.Textf("%d byte(s)", decodableReference.Value.GetSizeBytes()),
				),
			),
			h.Tr(
				h.Th(
					h.Class("whitespace-nowrap"),
					g.Text("Height:"),
				),
				h.Td(
					g.Textf("%d", decodableReference.Value.GetHeight()),
				),
			),
			h.Tr(
				h.Th(
					h.Class("whitespace-nowrap"),
					g.Text("Degree:"),
				),
				h.Td(
					g.Textf("%d outgoing reference(s)", decodableReference.Value.GetDegree()),
				),
			),
			h.Tr(
				h.Th(
					h.Class("whitespace-nowrap"),
					g.Text("Maximum total parents size:"),
				),
				h.Td(
					g.Textf("%d byte(s)", decodableReference.Value.GetMaximumTotalParentsSizeBytes(false)),
				),
			),
		),
	)
}

// renderEncoderSelector renders the list of recently observed
// encoders, followed by a form for adding a new encoder configuration.
// If encoderUsed is set, the first encoder was used to decode objects
// displayed on the page, and is highlighted.
//
// When decoding objects, the first encoder is tried, followed by any
// auto-detecting encoders (see mayTryEncoder()). A button for moving an
// encoder to the front of the list is only displayed if doing so may
// affect how the object referenced by the page is decoded. This is not
// the case for the first encoder, or for encoders that expect a
// different amount of decoding parameters. It is also not the case for
// auto-detecting encoders (e.g., ones that perform encryption), unless
// the first encoder is not auto-detecting. That encoder may
// successfully decode the object, even if it was encoded using a
// different configuration, meaning other encoders are never tried.
//
// The form is only used to add new configurations. It is not
// prefilled with the configuration that is currently in use, as that
// would reveal secrets (e.g., encryption keys). It only contains text
// if the configuration that was previously submitted was invalid.
func renderEncoderSelector(pageReference model_core.Decodable[object.GlobalReference], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder, encoderUsed bool) []g.Node {
	recentlyObservedEncodersNodes := []g.Node{
		h.Class("card bg-base-200 w-full p-4 shadow"),
		h.H1(
			h.Class("card-title text-2xl"),
			g.Text("Recently observed encoders"),
		),
	}
	// Whether the first encoder is not auto-detecting, meaning
	// other encoders may never be tried.
	shadowed := false
	for i, recentlyObservedEncoder := range recentlyObservedEncoders {
		applicable, autoDetecting := false, false
		if binaryEncoder, err := browserBinaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(
			recentlyObservedEncoder.GetConfiguration(),
			uint32(pageReference.Value.GetReferenceFormat().GetMaximumObjectSizeBytes()),
		); err == nil {
			applicable = binaryEncoder.GetDecodingParametersSizeBytes() == len(pageReference.GetDecodingParameters())
			autoDetecting = binaryEncoder.IsAutoDetecting()
		}
		offerUse := i > 0 && applicable && (!autoDetecting || shadowed)
		if i == 0 && applicable && !autoDetecting {
			shadowed = true
		}

		encoders := recentlyObservedEncoder.GetConfiguration()
		l := protoList[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder](encoders)
		compact, err := json.Marshal(l)
		if err != nil {
			continue
		}
		configuration := make([]protoreflect.Message, 0, len(encoders))
		for _, encoder := range encoders {
			configuration = append(configuration, encoder.ProtoReflect())
		}
		configurationRenderer := newMessageRenderer("", nil, false)
		configurationRenderer.width = sidebarMessageRenderWidth

		isUsed := i == 0 && encoderUsed
		cardClass := "card bg-base-100 shadow-sm w-full mt-4 p-4 text-left overflow-x-hidden"
		if isUsed {
			cardClass += " border-2 border-primary"
		}
		cardBody := []g.Node{h.Class(cardClass)}
		titleText := "User provided"
		if o := recentlyObservedEncoder.GetLastObservation(); o != nil {
			titleText = o.GetMessageType() + "." + o.GetFieldName()
		}
		var titleAction g.Node
		if isUsed {
			titleAction = h.Span(h.Class("badge badge-primary"), g.Text("In use"))
		} else if offerUse {
			titleAction = h.Button(h.Class("btn btn-primary btn-sm"), g.Text("Use"))
		}
		cardBody = append(
			cardBody,
			h.Div(
				h.Class("flex justify-between items-start gap-2"),
				h.H2(h.Class("text-xl break-all"), g.Text(titleText)),
				titleAction,
			),
		)
		if o := recentlyObservedEncoder.GetLastObservation(); o != nil {
			cardBody = append(
				cardBody,
				h.P(
					h.Class("text-xs"),
					g.Text("Last observed: "),
					g.Text(o.GetTime().AsTime().Format(time.RFC3339)),
				),
			)
		}
		cardBody = append(
			cardBody,
			h.Div(
				append(
					[]g.Node{
						h.Class("block card my-2 p-4 bg-neutral message-contents text-neutral-content font-mono text-sm h-auto! overflow-x-auto"),
					},
					configurationRenderer.renderMessageList(
						model_core.NewSimpleMessage[object.LocalReference](configuration),
					)...,
				)...,
			),
		)

		recentlyObservedEncodersNodes = append(
			recentlyObservedEncodersNodes,
			h.Form(
				h.Method("post"),
				h.Input(
					h.Type("hidden"),
					h.Name("encoder_configuration"),
					h.Value(string(compact)),
				),
				h.Div(cardBody...),
			),
		)
	}

	return []g.Node{
		h.Div(recentlyObservedEncodersNodes...),

		h.Div(
			h.Class("card bg-base-200 w-full p-4 shadow"),
			h.H1(
				h.Class("card-title text-2xl mb-4"),
				g.Text("New encoder configuration"),
			),

			h.Form(
				h.Method("post"),
				h.Textarea(
					h.Class("font-mono textarea w-full"),
					h.Name("encoder_configuration"),
					h.Rows("10"),
					h.Placeholder(`[
  {
    "zstdCompressing": {}
  },
  {
    "encrypting": {
      "encryptionKey": "..."
    }
  }
]`),
				),
				h.Div(
					h.Class("card-actions justify-end mt-4"),
					h.Button(
						h.Class("btn btn-primary"),
						g.Text("Add"),
					),
				),
			),
		),
	}
}

// renderEvaluationPage renders a HTML page for displaying the contents
// of an evaluation of a key stored in an evaluations list.
func renderEvaluationPage(
	w http.ResponseWriter,
	evaluationsListReference model_core.Decodable[object.GlobalReference],
	formatTabs [][]g.Node,
	currentFormatIndex int,
	keyNodes []g.Node,
	valueNodes []g.Node,
	dependenciesNodes []g.Node,
	encoderUsed bool,
	cookie *browser_pb.Cookie,
) g.Node {
	setCookie(w, cookie)

	evaluationCard := []g.Node{
		h.Class("card bg-base-200 message-contents w-2/3 p-4 shadow"),
		h.H1(
			h.Class("card-title text-2xl mb-4"),
			g.Text("Evaluation"),
		),

		h.H2(
			h.Class("text-xl my-2"),
			g.Text("Key"),
		),
		h.Div(
			h.Class("my-2"),
			renderTabsLiftWithNeutralContent(formatTabs, currentFormatIndex, keyNodes),
		),

		h.H2(
			h.Class("text-xl my-2"),
			g.Text("Value"),
		),
	}
	evaluationCard = append(evaluationCard, valueNodes...)

	evaluationCard = append(
		evaluationCard,
		h.H2(
			h.Class("text-xl my-2"),
			g.Text("Dependencies"),
		),
	)
	evaluationCard = append(evaluationCard, dependenciesNodes...)

	return renderPage("TODO: Pick title", []g.Node{
		h.Div(
			h.Class("flex w-full space-x-4 p-4"),

			h.Div(append(
				[]g.Node{
					h.Class("flex flex-col w-1/3 space-y-4"),
					renderReferenceCard("Evaluations list reference", evaluationsListReference),
				},
				renderEncoderSelector(evaluationsListReference, cookie.GetRecentlyObservedEncoders(), encoderUsed)...,
			)...),

			h.Div(evaluationCard...),
		),
	})
}

// evaluationFormat is a format in which keys and values can be
// displayed on the evaluation page.
type evaluationFormat struct {
	queryParameter string
	name           string
	pretty         bool
}

// evaluationFormats lists the formats in which keys and values can be
// displayed on the evaluation page. The last format is the default.
var evaluationFormats = []evaluationFormat{
	{queryParameter: "json", name: "JSON", pretty: false},
	{queryParameter: "pretty", name: "Pretty", pretty: true},
}

func (s *BrowserService) doEvaluation(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	evaluationsListReference, err := getReferenceFromRequest(r)
	if err != nil {
		return nil, err
	}
	referenceFormat := evaluationsListReference.Value.GetReferenceFormat()

	// The final component of the URL contains the key to display.
	// Before it, there may be references of keys whose graphlets
	// contain the key to display.
	keyPath := strings.Split(r.PathValue("key_path"), "/")
	parentKeyPath := keyPath[:len(keyPath)-1]
	parentKeyReferences := make([]object.LocalReference, 0, len(parentKeyPath))
	for _, pathSegment := range parentKeyPath {
		keyReferenceBytes, err := base64.RawURLEncoding.DecodeString(pathSegment)
		if err != nil {
			return nil, err
		}
		keyReference, err := referenceFormat.NewLocalReference(keyReferenceBytes)
		if err != nil {
			return nil, err
		}
		parentKeyReferences = append(parentKeyReferences, keyReference)
	}

	keyBytes, err := base64.RawURLEncoding.DecodeString(keyPath[len(keyPath)-1])
	if err != nil {
		return nil, err
	}
	keyAny, err := model_core.UnmarshalTopLevelMessage[model_core_pb.TopLevelAny](referenceFormat, keyBytes)
	if err != nil {
		return nil, err
	}
	keyReference, err := model_core.ComputeTopLevelMessageReference(keyAny, referenceFormat)
	if err != nil {
		return nil, err
	}

	// Keys, values and dependencies can either be rendered as JSON,
	// or pretty printed. The choice applies to all of them.
	currentFormatIndex := len(evaluationFormats) - 1
	formatParameter := r.URL.Query().Get("format")
	for i, format := range evaluationFormats {
		if format.queryParameter == formatParameter {
			currentFormatIndex = i
			break
		}
	}
	formatTabs := make([][]g.Node, 0, len(evaluationFormats))
	for _, format := range evaluationFormats {
		formatTabs = append(formatTabs, []g.Node{
			h.Href("?format=" + format.queryParameter),
			g.Text(format.name),
		})
	}

	jsonRenderer := newMessageRenderer(
		path.Join(
			strings.Repeat("../", len(parentKeyReferences))+"../../../../object",
			evaluationsListReference.Value.InstanceName.AsURLSafeComponent(),
			referenceFormat.ToProto().String(),
		),
		&referenceFormat,
		evaluationFormats[currentFormatIndex].pretty,
	)
	keyNodes := jsonRenderer.renderTopLevelMessage(
		model_core.NewTopLevelMessage(keyAny.Message.ProtoReflect(), keyAny.OutgoingReferences),
	)

	// Whether the first encoder in the cookie was used to decode
	// objects displayed on the page.
	encoderUsed := false
	cookie := getEncodersFromRequest(r)

	parsedObjectPoolIngester := model_parser.NewParsedObjectPoolIngester(
		s.parsedObjectPool,
		model_parser.NewDownloadingObjectReader(
			object_namespacemapping.NewNamespaceAddingDownloader(s.objectDownloader, evaluationsListReference.Value.InstanceName),
		),
	)

	// Attempt to decode the evaluations list using each of the
	// recently observed encoders. Use the first encoder that
	// succeeds for decoding all other objects as well.
	ctx := r.Context()
	var evaluationReader model_parser.DecodingObjectReader[object.LocalReference, model_core.Message[*model_evaluation_pb.Evaluation, object.LocalReference]]
	var evaluationsReader model_parser.DecodingObjectReader[object.LocalReference, model_core.Message[[]*model_evaluation_pb.Evaluations, object.LocalReference]]
	var keysReader model_parser.DecodingObjectReader[object.LocalReference, model_core.Message[[]*model_evaluation_pb.Keys, object.LocalReference]]
	var evaluationsList model_core.Message[[]*model_evaluation_pb.Evaluations, object.LocalReference]
	usedEncoderIndex := -1
	for i, recentlyObservedEncoder := range cookie.GetRecentlyObservedEncoders() {
		binaryEncoder, encoderErr := browserBinaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(
			recentlyObservedEncoder.GetConfiguration(),
			uint32(referenceFormat.GetMaximumObjectSizeBytes()),
		)
		if encoderErr != nil {
			err = fmt.Errorf("failed to create encoder: %w", encoderErr)
			continue
		}
		if !mayTryEncoder(i, binaryEncoder) {
			continue
		}
		candidateEvaluationsReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](binaryEncoder),
				model_parser.NewProtoListObjectParser[object.LocalReference, model_evaluation_pb.Evaluations](),
			),
		)
		candidateEvaluationsList, readErr := candidateEvaluationsReader.ReadObject(
			ctx,
			model_core.CopyDecodable(evaluationsListReference, evaluationsListReference.Value.LocalReference),
		)
		if readErr != nil {
			err = fmt.Errorf("failed to download and decode evaluations list object: %w", readErr)
			continue
		}

		evaluationReader = model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](binaryEncoder),
				model_parser.NewProtoObjectParser[object.LocalReference, model_evaluation_pb.Evaluation](),
			),
		)
		evaluationsReader = candidateEvaluationsReader
		keysReader = model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](binaryEncoder),
				model_parser.NewProtoListObjectParser[object.LocalReference, model_evaluation_pb.Keys](),
			),
		)
		evaluationsList = candidateEvaluationsList
		usedEncoderIndex = i
		break
	}
	if usedEncoderIndex < 0 {
		errNodes := renderErrorAlert(err)
		return renderEvaluationPage(
			w,
			evaluationsListReference,
			formatTabs,
			currentFormatIndex,
			keyNodes,
			errNodes,
			errNodes,
			encoderUsed,
			cookie,
		), nil
	}

	// For future page views, put the encoder that was used to
	// decode the evaluations list first.
	recentlyObservedEncoders := cookie.GetRecentlyObservedEncoders()
	cookie.SetRecentlyObservedEncoders(append(
		append(
			[]*browser_pb.RecentlyObservedEncoder{recentlyObservedEncoders[usedEncoderIndex]},
			recentlyObservedEncoders[:usedEncoderIndex]...,
		),
		recentlyObservedEncoders[usedEncoderIndex+1:]...,
	))
	encoderUsed = true

	// If the key to display is contained within a graphlet, we must
	// first look up the graphlet.
	for _, parentKeyReference := range parentKeyReferences {
		evaluations, err := btree.Find(
			ctx,
			evaluationsReader,
			evaluationsList,
			func(entry model_core.Message[*model_evaluation_pb.Evaluations, object.LocalReference]) (int, *model_core_pb.DecodableReference) {
				switch entry.Message.WhichLevel() {
				case model_evaluation_pb.Evaluations_Leaf_case:
					return bytes.Compare(parentKeyReference.GetRawReference(), entry.Message.GetLeaf().GetKeyReference()), nil
				case model_evaluation_pb.Evaluations_Parent_case:
					return bytes.Compare(parentKeyReference.GetRawReference(), entry.Message.GetParent().GetFirstKeyReference()), entry.Message.GetParent().GetReference()
				default:
					return 0, nil
				}
			},
		)
		if err != nil {
			errNodes := renderErrorAlert(fmt.Errorf("failed to look up key in evaluations list: %w", err))
			return renderEvaluationPage(
				w,
				evaluationsListReference,
				formatTabs,
				currentFormatIndex,
				keyNodes,
				errNodes,
				errNodes,
				encoderUsed,
				cookie,
			), nil
		}

		if !evaluations.IsSet() {
			errNodes := renderErrorAlert(fmt.Errorf("parent %s cannot be found in evaluations list", base64.RawURLEncoding.EncodeToString(parentKeyReference.GetRawReference())))
			return renderEvaluationPage(
				w,
				evaluationsListReference,
				formatTabs,
				currentFormatIndex,
				keyNodes,
				errNodes,
				errNodes,
				encoderUsed,
				cookie,
			), nil
		}
		evaluationsLeaf := evaluations.Message.GetLeaf()
		if evaluationsLeaf == nil {
			errNodes := renderErrorAlert(errors.New("evaluations list entry is not a valid leaf"))
			return renderEvaluationPage(
				w,
				evaluationsListReference,
				formatTabs,
				currentFormatIndex,
				keyNodes,
				errNodes,
				errNodes,
				encoderUsed,
				cookie,
			), nil
		}
		evaluationsList = model_core.Nested(evaluations, evaluationsLeaf.GetGraphlet().GetDependencyEvaluations())
	}

	evaluations, err := btree.Find(
		ctx,
		evaluationsReader,
		evaluationsList,
		func(entry model_core.Message[*model_evaluation_pb.Evaluations, object.LocalReference]) (int, *model_core_pb.DecodableReference) {
			switch entry.Message.WhichLevel() {
			case model_evaluation_pb.Evaluations_Leaf_case:
				return bytes.Compare(keyReference.GetRawReference(), entry.Message.GetLeaf().GetKeyReference()), nil
			case model_evaluation_pb.Evaluations_Parent_case:
				return bytes.Compare(keyReference.GetRawReference(), entry.Message.GetParent().GetFirstKeyReference()), entry.Message.GetParent().GetReference()
			default:
				return 0, nil
			}
		},
	)
	if err != nil {
		errNodes := renderErrorAlert(fmt.Errorf("failed to look up key in evaluations list: %w", err))
		return renderEvaluationPage(
			w,
			evaluationsListReference,
			formatTabs,
			currentFormatIndex,
			keyNodes,
			errNodes,
			errNodes,
			encoderUsed,
			cookie,
		), nil
	}

	valueNodes := renderWarningAlert("Evaluation failed to yield a value.")
	dependenciesNodes := renderWarningAlert("This key has no dependencies, or evaluation failed to yield a list of dependencies.")
	if evaluations.IsSet() {
		evaluationsLeaf := evaluations.Message.GetLeaf()
		if evaluationsLeaf == nil {
			errNodes := renderErrorAlert(errors.New("evaluations list entry is not a valid leaf"))
			return renderEvaluationPage(
				w,
				evaluationsListReference,
				formatTabs,
				currentFormatIndex,
				keyNodes,
				errNodes,
				errNodes,
				encoderUsed,
				cookie,
			), nil
		}

		graphlet := model_core.Nested(evaluations, evaluationsLeaf.GetGraphlet())
		evaluation, err := model_evaluation.GraphletGetEvaluation(ctx, evaluationReader, graphlet)
		if err != nil {
			errNodes := renderErrorAlert(err)
			return renderEvaluationPage(
				w,
				evaluationsListReference,
				formatTabs,
				currentFormatIndex,
				keyNodes,
				errNodes,
				errNodes,
				encoderUsed,
				cookie,
			), nil
		}

		if v := evaluation.Message.GetValue(); v != nil {
			// Values are stored without type information.
			// Attempt to derive the type from the key. If
			// that fails, display the raw message.
			renderedValue := model_core.Nested(evaluation, v.ProtoReflect())
			if valueMessageType, err := model_evaluation.GetValueMessageType(protoreflect.FullName(keyAny.Message.GetFormat().GetProtoTypeName())); err == nil {
				if value, err := model_core.UnmarshalOpaque(model_core.Nested(evaluation, v), model_format.NewDynamicProto(valueMessageType)); err == nil {
					renderedValue = model_core.NewMessage(value.Message.ProtoReflect(), value.OutgoingReferences)
				}
			}
			valueNodes = []g.Node{
				h.Div(
					h.Class("my-2"),
					renderTabsLiftWithNeutralContent(
						formatTabs,
						currentFormatIndex,
						jsonRenderer.renderMessage(renderedValue),
					),
				),
			}
		}

		var errIter error
		var nodes []g.Node
		for dependency := range btree.AllLeaves(
			ctx,
			keysReader,
			model_core.Nested(evaluation, evaluation.Message.GetDirectVariableDependencyKeys()),
			func(element model_core.Message[*model_evaluation_pb.Keys, object.LocalReference]) (*model_core_pb.DecodableReference, error) {
				return element.Message.GetParent().GetReference(), nil
			},
			&errIter,
		) {
			if dependencyLeaf := dependency.Message.GetLeaf(); dependencyLeaf != nil {
				cardNodes := []g.Node{
					h.Class("block card my-2 p-4 bg-neutral text-neutral-content font-mono h-auto! overflow-x-auto"),
				}
				if dependencyKey, err := model_core.FlattenAny(model_core.Nested(dependency, dependencyLeaf)); err == nil {
					if marshaledDependencyKey, err := model_core.MarshalTopLevelMessage(dependencyKey); err == nil {
						formNodes := []g.Node{
							h.Action(base64.RawURLEncoding.EncodeToString(keyReference.GetRawReference()) + "/" + base64.RawURLEncoding.EncodeToString(marshaledDependencyKey)),
							h.Button(
								h.Class("btn btn-primary btn-square float-right inline-block"),
								g.Text("↗"),
							),
						}
						if formatParameter != "" {
							// Submitting a form discards the
							// query of the action URL. Pass the
							// format along explicitly.
							formNodes = append(formNodes, h.Input(
								h.Type("hidden"),
								h.Name("format"),
								h.Value(formatParameter),
							))
						}
						cardNodes = append(cardNodes, h.Form(formNodes...))
					}
				}
				cardNodes = append(
					cardNodes,
					jsonRenderer.renderMessage(model_core.Nested(dependency, dependencyLeaf.ProtoReflect()))...,
				)
				nodes = append(nodes, h.Div(cardNodes...))
			}
		}
		if errIter == nil {
			dependenciesNodes = nodes
		} else {
			dependenciesNodes = renderErrorAlert(errIter)
		}
	} else if len(parentKeyReferences) > 0 {
		// The requested key is not present in the current
		// graphlet. However, it may be the case that it's
		// present in one of its parents. Perform a redirect to
		// the same key within the parent graphlet.
		redirectURL := url.URL{
			Path:     "../" + base64.RawURLEncoding.EncodeToString(keyBytes),
			RawQuery: r.URL.RawQuery,
		}
		http.Redirect(w, r, redirectURL.String(), http.StatusMovedPermanently)
		return nil, nil
	}

	// Rendering values might reveal the existence of additional encoders.
	recentlyObservedEncoders = cookie.GetRecentlyObservedEncoders()
	cookie.SetRecentlyObservedEncoders(trimRecentlyObservedEncoders(
		append(
			append(
				[]*browser_pb.RecentlyObservedEncoder{recentlyObservedEncoders[0]},
				jsonRenderer.observedEncoders...,
			),
			recentlyObservedEncoders[1:]...,
		),
	))
	return renderEvaluationPage(
		w,
		evaluationsListReference,
		formatTabs,
		currentFormatIndex,
		keyNodes,
		valueNodes,
		dependenciesNodes,
		encoderUsed,
		cookie,
	), nil
}

// renderObjectPage renders a HTML page for displaying the contents of
// an object.
func renderObjectPage(
	w http.ResponseWriter,
	decodableReference model_core.Decodable[object.GlobalReference],
	payloadRenderers []payloadRenderer,
	currentPayloadRendererIndex int,
	encoderUsed bool,
	cookie *browser_pb.Cookie,
	titlePrefix string,
	payload []g.Node,
) g.Node {
	setCookie(w, cookie)

	formatTabs := make([][]g.Node, 0, len(payloadRenderers))
	for _, payloadRenderer := range payloadRenderers {
		formatTabs = append(formatTabs, []g.Node{
			h.Href("?format=" + payloadRenderer.queryParameter()),
			g.Text(payloadRenderer.name()),
		})
	}

	rawReference := base64.RawURLEncoding.EncodeToString(decodableReference.Value.GetRawReference())
	return renderPage(titlePrefix+rawReference, []g.Node{
		h.Div(
			h.Class("flex w-full space-x-4 p-4"),

			h.Div(append(
				[]g.Node{
					h.Class("flex flex-col w-1/3 space-y-4"),
					renderReferenceCard("Object reference", decodableReference),
				},
				renderEncoderSelector(decodableReference, cookie.GetRecentlyObservedEncoders(), encoderUsed)...,
			)...),

			h.Div(
				h.Class("card bg-base-200 message-contents w-2/3 p-4 shadow"),
				h.H1(
					h.Class("card-title text-2xl mb-4"),
					g.Text("Payload"),
				),
				renderTabsLiftWithNeutralContent(
					formatTabs,
					currentPayloadRendererIndex,
					payload,
				),
			),
		),
	})
}

// renderErrorAlert renders a Go error in the form of a red alert banner.
func renderErrorAlert(err error) []g.Node {
	return []g.Node{
		h.Div(
			h.Class("alert alert-error shadow-sm"),
			g.Text(err.Error()),
		),
	}
}

// renderWarningAlert renders a warning message in the form of a yellow
// alert banner.
func renderWarningAlert(message string) []g.Node {
	return []g.Node{
		h.Div(
			h.Class("alert alert-warning shadow-sm"),
			g.Text(message),
		),
	}
}

func (s *BrowserService) doObject(
	w http.ResponseWriter,
	r *http.Request,
	payloadRenderers []payloadRenderer,
	defaultPayloadRendererIndex int,
	titlePrefix string,
) (g.Node, error) {
	objectReference, err := getReferenceFromRequest(r)
	if err != nil {
		return nil, err
	}

	currentPayloadRendererIndex := defaultPayloadRendererIndex
	formatParameter := r.URL.Query().Get("format")
	for i, payloadRenderer := range payloadRenderers {
		if payloadRenderer.queryParameter() == formatParameter {
			currentPayloadRendererIndex = i
			break
		}
	}

	// Whether the first encoder in the cookie was used to decode
	// objects displayed on the page.
	encoderUsed := false
	cookie := getEncodersFromRequest(r)

	// Fetch and render the object.
	o, err := s.objectDownloader.DownloadObject(r.Context(), objectReference.Value)
	if err != nil {
		return renderObjectPage(
			w,
			objectReference,
			payloadRenderers,
			currentPayloadRendererIndex,
			encoderUsed,
			cookie,
			titlePrefix,
			renderErrorAlert(fmt.Errorf("failed to download object: %w", err)),
		), nil
	}
	recentlyObservedEncoders := cookie.GetRecentlyObservedEncoders()
	rendered, usedEncoderIndex, encodersInObject := payloadRenderers[currentPayloadRendererIndex].render(
		r,
		model_core.CopyDecodable(objectReference, o),
		recentlyObservedEncoders,
	)

	// For future page views, change the order of encoders to:
	// - The encoder that was used to decrypt the object.
	// - Any encoders found in the object that is currently being
	//   displayed.
	// - All existing encoders that were not used this time.
	if usedEncoderIndex >= 0 {
		cookie.SetRecentlyObservedEncoders(trimRecentlyObservedEncoders(
			append(
				append(
					append(
						[]*browser_pb.RecentlyObservedEncoder{recentlyObservedEncoders[usedEncoderIndex]},
						encodersInObject...,
					),
					recentlyObservedEncoders[:usedEncoderIndex]...,
				),
				recentlyObservedEncoders[usedEncoderIndex+1:]...,
			),
		))
		encoderUsed = true
	}

	return renderObjectPage(
		w,
		objectReference,
		payloadRenderers,
		currentPayloadRendererIndex,
		encoderUsed,
		cookie,
		titlePrefix,
		rendered,
	), nil
}

func (s *BrowserService) doProtoObject(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	payloadRenderers := []payloadRenderer{
		rawPayloadRenderer{},
		decodedPayloadRenderer{},
		messagePayloadRenderer{pretty: false},
		messagePayloadRenderer{pretty: true},
	}
	return s.doObject(
		w,
		r,
		payloadRenderers,
		/* defaultPayloadRendererIndex = */ len(payloadRenderers)-1,
		r.PathValue("message_type")+" ",
	)
}

func (s *BrowserService) doProtoListObject(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	payloadRenderers := []payloadRenderer{
		rawPayloadRenderer{},
		decodedPayloadRenderer{},
		messageListPayloadRenderer{pretty: false},
		messageListPayloadRenderer{pretty: true},
	}
	return s.doObject(
		w,
		r,
		payloadRenderers,
		/* defaultPayloadRendererIndex = */ len(payloadRenderers)-1,
		r.PathValue("message_type")+" list ",
	)
}

func (s *BrowserService) doRawObject(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	payloadRenderers := []payloadRenderer{
		rawPayloadRenderer{},
		decodedPayloadRenderer{},
		textPayloadRenderer{},
	}
	return s.doObject(
		w,
		r,
		payloadRenderers,
		/* defaultPayloadRendererIndex = */ len(payloadRenderers)-1,
		"",
	)
}

func marshalAndURLEncode(m proto.Message) (string, error) {
	data, err := proto.Marshal(m)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func timeoutToText(timeout *timestamppb.Timestamp, now time.Time) string {
	if timeout == nil {
		return "∞"
	}
	if timeout.CheckValid() != nil {
		return "?"
	}
	diff := timeout.AsTime().Sub(now)
	if diff < 0 {
		diff = 0
	}
	return diff.Truncate(time.Second).String()
}

func (s *BrowserService) doPlatformQueues(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	now := time.Now()
	ctx := r.Context()
	operationsCount, err := s.buildQueueStateClient.ListOperations(ctx, &buildqueuestate_pb.ListOperationsRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list platform queues: %w", err)
	}
	response, err := s.buildQueueStateClient.ListPlatformQueues(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, fmt.Errorf("failed to list platform queues: %w", err)
	}
	platformQueues := response.GetPlatformQueues()
	tableRows := make([]g.Node, 0, len(platformQueues))
	for _, pq := range platformQueues {
		for i, scq := range pq.GetSizeClassQueues() {
			// For each platform queue, add a multi-row
			// column on the left hand side containing the
			// PKIX public keys.
			var cells []g.Node
			if i == 0 {
				encodedPkixPublicKeys := pq.GetPkixPublicKeys()
				pkixPublicKeys := append(
					make([]g.Node, 0, 2+2*len(encodedPkixPublicKeys)),
					h.Class("break-all font-mono"),
					h.RowSpan(strconv.FormatInt(int64(len(pq.GetSizeClassQueues())), 10)),
				)
				for _, pkixPublicKey := range encodedPkixPublicKeys {
					pkixPublicKeys = append(
						pkixPublicKeys,
						g.Text(base64.StdEncoding.EncodeToString(pkixPublicKey)),
						h.Br(),
					)
				}
				cells = append(cells, h.Td(pkixPublicKeys...))
			}

			sizeClassQueueName := buildqueuestate_pb.SizeClassQueueName_builder{
				PlatformPkixPublicKey: pq.GetPkixPublicKeys()[0],
				SizeClass:             scq.GetSizeClass(),
			}.Build()
			sizeClassQueueNameStr, err := marshalAndURLEncode(sizeClassQueueName)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal and encode size class queue name: %w", err)
			}
			invocationName := buildqueuestate_pb.InvocationName_builder{
				SizeClassQueueName: sizeClassQueueName,
			}.Build()
			invocationNameStr, err := marshalAndURLEncode(invocationName)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal and encode invocation name: %w", err)
			}
			listWorkersFilterExecutingStr, err := marshalAndURLEncode(buildqueuestate_pb.ListWorkersRequest_Filter_builder{
				Executing: proto.ValueOrDefault(invocationName),
			}.Build())
			if err != nil {
				return nil, fmt.Errorf("failed to marshal and encode executing workers filter: %w", err)
			}
			listWorkersFilterIdleSynchronizingStr, err := marshalAndURLEncode(buildqueuestate_pb.ListWorkersRequest_Filter_builder{
				IdleSynchronizing: proto.ValueOrDefault(invocationName),
			}.Build())
			if err != nil {
				return nil, fmt.Errorf("failed to marshal and encode idle synchronizing workers filter: %w", err)
			}
			listWorkersFilterAllStr, err := marshalAndURLEncode(buildqueuestate_pb.ListWorkersRequest_Filter_builder{
				All: proto.ValueOrDefault(sizeClassQueueName),
			}.Build())
			if err != nil {
				return nil, fmt.Errorf("failed to marshal and encode all workers filter: %w", err)
			}

			// Counters that are tracked per size class queue.
			tableRows = append(
				tableRows,
				h.Tr(
					append(
						cells,
						h.Td(h.Class("text-right"), g.Textf("%d", scq.GetSizeClass())),
						h.Td(h.Class("text-right"), g.Text(timeoutToText(scq.GetTimeout(), now))),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("queued_operations/"+invocationNameStr),
								g.Textf("%d", scq.GetRootInvocation().GetQueuedOperationsCount()),
							),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("invocation_children/"+invocationNameStr+"/QUEUED"),
								g.Textf("%d", scq.GetRootInvocation().GetQueuedChildrenCount()),
							),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("invocation_children/"+invocationNameStr+"/ACTIVE"),
								g.Textf("%d", scq.GetRootInvocation().GetActiveChildrenCount()),
							),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("invocation_children/"+invocationNameStr+"/ALL"),
								g.Textf("%d", scq.GetRootInvocation().GetChildrenCount()),
							),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("workers/"+listWorkersFilterExecutingStr),
								g.Textf("%d", scq.GetRootInvocation().GetExecutingWorkersCount()),
							),
						),
						h.Td(
							h.Class("text-right"),
							g.Textf("%d", scq.GetRootInvocation().GetIdleWorkersCount()),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("workers/"+listWorkersFilterIdleSynchronizingStr),
								g.Textf("%d", scq.GetRootInvocation().GetIdleSynchronizingWorkersCount()),
							),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("workers/"+listWorkersFilterAllStr),
								g.Textf("%d", scq.GetWorkersCount()),
							),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("drains/"+sizeClassQueueNameStr),
								g.Textf("%d", scq.GetDrainsCount()),
							),
						),
					)...,
				),
			)
		}
	}

	return renderPage("Build queue", []g.Node{
		h.Div(
			h.Class("p-4"),

			h.Div(
				h.Class("card bg-base-200 p-4 shadow"),
				h.H1(
					h.Class("card-title text-2xl mb-4"),
					g.Text("Build queue"),
				),
				h.Table(
					h.Class("table table-fixed"),
					h.Tr(
						h.Th(
							h.Class("w-1/4"),
							g.Text("Total number of operations:"),
						),
						h.Td(
							h.Class("w-3/4"),
							h.A(
								h.Class("link link-primary"),
								h.Href("operations/ALL"),
								g.Textf("%d", operationsCount.GetPaginationInfo().GetTotalEntries()),
							),
						),
					),
				),
			),

			h.Div(
				h.Class("card bg-base-200 p-4 shadow my-4"),
				h.H1(
					h.Class("card-title text-2xl mb-4"),
					g.Text("Platform queues"),
				),
				h.Table(
					h.Class("table"),
					h.THead(
						h.Class("text-center"),
						h.Tr(
							h.Th(
								h.RowSpan("3"),
								g.Text("PKIX public keys"),
							),
							h.Th(
								h.RowSpan("3"),
								g.Text("Size class"),
							),
							h.Th(
								h.RowSpan("3"),
								g.Text("Timeout"),
							),
							h.Th(
								h.ColSpan("7"),
								g.Text("Root invocation"),
							),
							h.Th(
								h.RowSpan("3"),
								g.Text("All workers"),
							),
							h.Th(
								h.RowSpan("3"),
								g.Text("Drains"),
							),
						),
						h.Tr(
							h.Th(
								h.RowSpan("2"),
								g.Text("Queued operations"),
							),
							h.Th(
								h.ColSpan("3"),
								g.Text("Children"),
							),
							h.Th(
								h.ColSpan("3"),
								g.Text("Workers"),
							),
						),
						h.Tr(
							h.Th(g.Text("Queued")),
							h.Th(g.Text("Active")),
							h.Th(g.Text("All")),
							h.Th(g.Text("Executing")),
							h.Th(g.Text("Idle")),
							h.Th(g.Text("Idle synchronizing")),
						),
					),
					h.TBody(tableRows...),
				),
			),
		),
	}), nil
}

func (s *BrowserService) doOperation(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	operationName := r.PathValue("operation_name")
	response, err := s.buildQueueStateClient.GetOperation(r.Context(), buildqueuestate_pb.GetOperationRequest_builder{
		OperationName: operationName,
	}.Build())
	if err != nil {
		return nil, fmt.Errorf("failed to get operation: %w", err)
	}
	operation := response.GetOperation()

	invocationName := operation.GetInvocationName()
	sizeClassQueueName := invocationName.GetSizeClassQueueName()
	rawInvocationIDs := invocationName.GetIds()
	invocationIDs := make([]g.Node, 0, len(rawInvocationIDs))
	for _, rawInvocationID := range rawInvocationIDs {
		invocationIDs = append(
			invocationIDs,
			h.Li(
				h.A(
					h.Href("TODO"),
					g.Text(protojson.Format(rawInvocationID)),
				),
			),
		)
	}

	cookie := getCookie(r)
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("failed to parse form: %w", err)
	}
	if providedPrivateKey := r.FormValue("private_key"); providedPrivateKey != "" {
		privateKey, err := crypto.ParsePEMWithPKCS8ECDHPrivateKey([]byte(providedPrivateKey))
		if err != nil {
			return nil, fmt.Errorf("invalid private key: %w", err)
		}
		marshaledPrivateKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal private key: %w", err)
		}
		ecdhPrivateKeys := append([][]byte{marshaledPrivateKey}, cookie.GetEcdhPrivateKeys()...)
		if maximumCount := 10; len(ecdhPrivateKeys) > maximumCount {
			ecdhPrivateKeys = ecdhPrivateKeys[:maximumCount]
		}
		cookie.SetEcdhPrivateKeys(ecdhPrivateKeys)
	}

	action := operation.GetAction()
	if action == nil {
		return nil, errors.New("no action provided")
	}
	platformECDHPublicKey, err := crypto.ParsePKIXECDHPublicKey(action.GetPlatformPkixPublicKey())
	if err != nil {
		return nil, fmt.Errorf("action contains an invalid platform PKIX public key: %w", err)
	}
	clientCertificateChain := action.GetClientCertificateChain()
	if len(clientCertificateChain) == 0 {
		return nil, errors.New("action contains no client certificate chain")
	}
	clientCertificate, err := x509.ParseCertificate(clientCertificateChain[0])
	if err != nil {
		return nil, fmt.Errorf("action contains an invalid client certificate: %w", err)
	}
	clientECDHPublicKey, err := crypto.PublicKeyToECDHPublicKey(clientCertificate.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to extract public key from client certificate in action: %w", err)
	}

	var ecdhPrivateKey *ecdh.PrivateKey
	var ecdhPublicKey *ecdh.PublicKey
	for _, rawECDHPrivateKey := range cookie.GetEcdhPrivateKeys() {
		ecdhPrivateKey, err = crypto.ParsePKCS8ECDHPrivateKey(rawECDHPrivateKey)
		if err == nil {
			if ecdhPrivateKey.PublicKey().Equal(platformECDHPublicKey) {
				ecdhPublicKey = clientECDHPublicKey
				break
			} else if ecdhPrivateKey.PublicKey().Equal(clientECDHPublicKey) {
				ecdhPublicKey = platformECDHPublicKey
				break
			}
		}
	}

	var actionNode g.Node
	now := time.Now()
	if ecdhPublicKey == nil {
		platformPKIXPublicKey, _ := x509.MarshalPKIXPublicKey(platformECDHPublicKey)
		clientPKIXPublicKey, _ := x509.MarshalPKIXPublicKey(clientECDHPublicKey)
		actionNode = h.Div(
			h.P(
				g.Text("No ECDH private key available that can be used to decrypt this action. Please provide the private key corresponding to one of the following two PKIX public keys:"),
			),

			h.Ul(
				h.Class("list-disc list-inside m-4"),
				h.Li(
					h.B(g.Text("Client: ")),
					h.Span(
						h.Class("font-mono"),
						g.Text(base64.StdEncoding.EncodeToString(clientPKIXPublicKey)),
					),
				),
				h.Li(
					h.B(g.Text("Platform: ")),
					h.Span(
						h.Class("font-mono"),
						g.Text(base64.StdEncoding.EncodeToString(platformPKIXPublicKey)),
					),
				),
			),

			h.Form(
				h.Method("post"),
				h.Textarea(
					h.Class("font-mono textarea w-full"),
					h.Name("private_key"),
					h.Rows("10"),
					h.Placeholder(`-----BEGIN PRIVATE KEY-----
...
-----END PRIVATE KEY-----`),
				),
				h.Div(
					h.Class("card-actions justify-end mt-4"),
					h.Button(
						h.Class("btn btn-primary"),
						g.Text("Decrypt action"),
					),
				),
			),
		)
	} else {
		sharedSecret, err := ecdhPrivateKey.ECDH(ecdhPublicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to compute action shared secret: %w", err)
		}

		// Decrypt action message.
		actionPlaintext, err := encryptedaction.ActionGetPlaintext(action, sharedSecret)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt action: %w", err)
		}
		var actionMessage anypb.Any
		if err := proto.Unmarshal(actionPlaintext, &actionMessage); err != nil {
			return nil, fmt.Errorf("failed to unmarshal action: %w", err)
		}

		// Give special treatment to actions that use the
		// "executewithstorage" protocol. For these we can
		// extract the storage namespace and object format,
		// allowing us to make the action reference clickable.
		jsonRenderer := newMessageRenderer("", nil, false)
		jsonRenderer.now = now
		var executeWithStorageAction model_executewithstorage_pb.Action
		if actionMessage.UnmarshalTo(&executeWithStorageAction) == nil {
			namespace, err := object.NewNamespace(executeWithStorageAction.GetNamespace())
			if err != nil {
				return nil, fmt.Errorf("invalid namespace: %#v", err)
			}
			jsonRenderer.basePath = path.Join(
				"../../object",
				namespace.InstanceName.AsURLSafeComponent(),
				namespace.ReferenceFormat.ToProto().String(),
			)
			jsonRenderer.renderer.FallbackObjectFormat = executeWithStorageAction.GetActionFormat()
			jsonRenderer.renderer.ReferenceFormat = &namespace.ReferenceFormat
		}

		actionNode = h.Div(
			append(
				[]g.Node{
					h.Class("block card p-4 bg-neutral message-contents text-neutral-content font-mono h-auto! overflow-x-auto"),
				},
				jsonRenderer.renderTopLevelMessage(
					model_core.NewSimpleTopLevelMessage[object.LocalReference](actionMessage.ProtoReflect()),
				)...,
			)...,
		)
		cookie.SetRecentlyObservedEncoders(trimRecentlyObservedEncoders(
			append(
				append(
					[]*browser_pb.RecentlyObservedEncoder(nil),
					jsonRenderer.observedEncoders...,
				),
				cookie.GetRecentlyObservedEncoders()...,
			),
		))
	}

	setCookie(w, cookie)
	return renderPage("Operation", []g.Node{
		h.Div(
			h.Class("flex w-full space-x-4 p-4"),
			h.Div(
				h.Class("flex flex-col w-1/3 space-y-4"),
				h.Div(
					h.Class("card bg-base-200 p-4 shadow"),
					h.H1(
						h.Class("card-title text-2xl mb-4"),
						g.Text("Operation"),
					),

					h.Table(
						h.Class("table"),
						h.Tr(
							h.Th(
								h.Class("whitespace-nowrap"),
								g.Text("Name:"),
							),
							h.Td(
								h.Class("break-all font-mono"),
								g.Text(operationName),
							),
						),
						h.Tr(
							h.Th(
								h.Class("whitespace-nowrap"),
								g.Text("Platform PKIX public key:"),
							),
							h.Td(
								h.Class("break-all font-mono"),
								g.Text(base64.StdEncoding.EncodeToString(sizeClassQueueName.GetPlatformPkixPublicKey())),
							),
						),
						h.Tr(
							h.Th(
								h.Class("whitespace-nowrap"),
								g.Text("Size class:"),
							),
							h.Td(
								g.Textf("%d", sizeClassQueueName.GetSizeClass()),
							),
						),
						h.Tr(
							h.Th(
								h.Class("whitespace-nowrap"),
								g.Text("Invocation IDs:"),
							),
							h.Td(
								h.Class("break-all"),
								h.Ul(invocationIDs...),
							),
						),
						h.Tr(
							h.Th(
								h.Class("whitespace-nowrap"),
								g.Text("Timeout:"),
							),
							h.Td(g.Text(timeoutToText(operation.GetTimeout(), now))),
						),
						h.Tr(
							h.Th(
								h.Class("whitespace-nowrap"),
								g.Text("Priority:"),
							),
							h.Td(g.Textf("%d", operation.GetPriority())),
						),
						h.Tr(
							h.Th(
								h.Class("whitespace-nowrap"),
								g.Text("Expected duration:"),
							),
							h.Td(g.Text(operation.GetExpectedDuration().AsDuration().String())),
						),
					),
				),
			),

			h.Div(
				h.Class("w-2/3"),
				h.Div(
					h.Class("card bg-base-200 p-4 shadow"),
					h.H1(
						h.Class("card-title text-2xl mb-4"),
						g.Text("Action"),
					),
					actionNode,
				),
			),
		),
	}), nil
}

const pageSize = 1000

func (s *BrowserService) doOperations(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	query := r.URL.Query()
	var filterInvocationID *anypb.Any
	if filterInvocationIDString := query.Get("filter_invocation_id"); filterInvocationIDString != "" {
		var invocationID anypb.Any
		if err := protojson.Unmarshal([]byte(filterInvocationIDString), &invocationID); err != nil {
			return nil, fmt.Errorf("invalid filter invocation ID: %w", err)
		}
		filterInvocationID = &invocationID
	}
	filterStageValue, ok := buildqueuestate_pb.ListOperationsRequest_ExecutionStage_value[r.PathValue("filter_stage")]
	if !ok {
		return nil, errors.New("invalid filter stage")
	}
	filterStage := buildqueuestate_pb.ListOperationsRequest_ExecutionStage(filterStageValue)

	var startAfter *buildqueuestate_pb.ListOperationsRequest_StartAfter
	if startAfterParameter := query.Get("start_after"); startAfterParameter != "" {
		var startAfterMessage buildqueuestate_pb.ListOperationsRequest_StartAfter
		if err := protojson.Unmarshal([]byte(startAfterParameter), &startAfterMessage); err != nil {
			return nil, fmt.Errorf("failed to parse start after message: %w", err)
		}
		startAfter = &startAfterMessage
	}

	ctx := r.Context()
	response, err := s.buildQueueStateClient.ListOperations(ctx, buildqueuestate_pb.ListOperationsRequest_builder{
		FilterInvocationId: filterInvocationID,
		FilterStage:        filterStage,
		PageSize:           pageSize,
		StartAfter:         startAfter,
	}.Build())
	if err != nil {
		return nil, fmt.Errorf("failed to list operations: %w", err)
	}

	// Pagination bar for filtering operations by stage.
	filterStageNodes := []g.Node{
		h.Class("join"),
	}
	for _, stage := range []struct {
		value buildqueuestate_pb.ListOperationsRequest_ExecutionStage
		label string
	}{
		{buildqueuestate_pb.ListOperationsRequest_ALL, "All"},
		{buildqueuestate_pb.ListOperationsRequest_QUEUED, "Queued"},
		{buildqueuestate_pb.ListOperationsRequest_EXECUTING, "Executing"},
		{buildqueuestate_pb.ListOperationsRequest_COMPLETED, "Completed"},
	} {
		if filterStage == stage.value {
			filterStageNodes = append(filterStageNodes, h.Span(
				h.Role("button"),
				h.Class("join-item btn btn-active btn-primary"),
				g.Text(stage.label),
			))
		} else {
			filterStageNodes = append(filterStageNodes, h.A(
				h.Role("button"),
				h.Class("join-item btn"),
				h.Href(stage.value.String()),
				g.Text(stage.label),
			))
		}
	}

	paginationNodes := []g.Node{
		h.Class("join"),
	}
	operations := response.GetOperations()
	paginationInfo := response.GetPaginationInfo()
	startIndex := paginationInfo.GetStartIndex()
	if startIndex > 0 {
		paginationNodes = append(paginationNodes, h.A(
			h.Role("button"),
			h.Class("join-item btn"),
			h.Href(filterStage.String()),
			g.Text("◀◀"),
		))
	} else {
		paginationNodes = append(paginationNodes, h.Span(
			h.Role("button"),
			h.Class("join-item btn btn-disabled"),
			g.Text("◀◀"),
		))
	}
	paginationNodes = append(paginationNodes, h.Span(
		h.Role("button"),
		h.Class("join-item btn btn-active btn-primary"),
		g.Textf("Showing operations [%d, %d) of %d in total", startIndex, startIndex+uint32(len(operations)), paginationInfo.GetTotalEntries()),
	))
	if l := len(operations); l > 0 {
		paginationNodes = append(paginationNodes, h.A(
			h.Role("button"),
			h.Class("join-item btn"),
			h.Href("TODO"),
			g.Text("▶"),
		))
	} else {
		paginationNodes = append(paginationNodes, h.Span(
			h.Role("button"),
			h.Class("join-item btn btn-disabled"),
			g.Text("▶"),
		))
	}

	tableRows := make([]g.Node, 0, len(operations))
	now := time.Now()
	for _, operation := range operations {
		tableRows = append(
			tableRows,
			h.Tr(
				h.Td(
					h.Class("font-mono"),
					h.A(
						h.Class("link link-primary"),
						h.Href("../../operation/"+operation.GetName()),
						g.Text(operation.GetName()),
					),
				),
				h.Td(h.Class("text-right"), g.Text(timeoutToText(operation.GetTimeout(), now))),
			),
		)
	}

	return renderPage("Operations", []g.Node{
		h.Div(
			h.Class("card bg-base-200 m-4 p-4 shadow"),
			h.H1(
				h.Class("card-title text-2xl mb-4"),
				g.Text("Operations"),
			),
			h.Div(
				h.Class("flex justify-between"),
				h.Div(filterStageNodes...),
				h.Div(paginationNodes...),
			),

			h.Table(
				h.Class("table"),
				h.THead(
					h.Class("text-center"),
					h.Tr(
						h.Th(
							h.Class("font-mono"),
						),
						h.Th(
							g.Text("Operation timeout"),
						),
					),
				),
				h.TBody(tableRows...),
			),
		),
	}), nil
}

func (s *BrowserService) doWorkers(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	filterData, err := base64.RawURLEncoding.DecodeString(r.PathValue("filter"))
	if err != nil {
		return nil, fmt.Errorf("failed to decode filter: %w", err)
	}
	var filter buildqueuestate_pb.ListWorkersRequest_Filter
	if err := proto.Unmarshal(filterData, &filter); err != nil {
		return nil, fmt.Errorf("failed to unmarshal filter: %w", err)
	}

	now := time.Now()
	response, err := s.buildQueueStateClient.ListWorkers(r.Context(), buildqueuestate_pb.ListWorkersRequest_builder{
		Filter:   &filter,
		PageSize: pageSize,
		// StartAfter: startAfter,
	}.Build())
	if err != nil {
		return nil, fmt.Errorf("failed to list workers: %w", err)
	}

	workers := response.GetWorkers()
	tableRows := make([]g.Node, 0, len(workers))
	for _, worker := range workers {
		id := worker.GetId()
		workerID := make([]g.Node, 0, 2*len(id))
		for _, key := range slices.Sorted(maps.Keys(id)) {
			workerID = append(
				workerID,
				h.Span(
					h.Class("badge badge-primary text-nowrap"),
					g.Textf("%s=%#v", key, id[key]),
				),
				g.Text(" "),
			)
		}
		cells := []g.Node{
			h.Td(workerID...),
			h.Td(h.Class("text-right"), g.Text(timeoutToText(worker.GetTimeout(), now))),
		}
		if operation := worker.GetCurrentOperation(); operation != nil {
			cells = append(
				cells,
				h.Td(
					h.Class("font-mono"),
					h.A(
						h.Class("link link-primary"),
						h.Href("../operation/"+operation.GetName()),
						g.Text(operation.GetName()),
					),
				),
				h.Td(h.Class("text-right"), g.Text(timeoutToText(operation.GetTimeout(), now))),
			)
		} else {
			cellText := "idle"
			if worker.GetDrained() {
				cellText = "drained"
			}
			cells = append(cells, h.Td(
				h.Class("text-center"),
				h.ColSpan("2"),
				g.Text(cellText),
			))
		}
		tableRows = append(tableRows, h.Tr(cells...))
	}

	return renderPage("Workers", []g.Node{
		h.Div(
			h.Class("w-full p-4"),

			h.Div(
				h.Class("card bg-base-200 p-4 shadow"),
				h.H1(
					h.Class("card-title text-2xl mb-4"),
					g.Text("Workers"),
				),
				h.Table(
					h.Class("table"),
					h.THead(
						h.Class("text-center"),
						h.Tr(
							h.Th(
								g.Text("Worker ID"),
							),
							h.Th(
								g.Text("Worker timeout"),
							),
							h.Th(
								g.Text("Operation name"),
							),
							h.Th(
								g.Text("Operation timeout"),
							),
						),
					),
					h.TBody(tableRows...),
				),
			),
		),
	}), nil
}

type protoList[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
] []*TMessage

var (
	_ json.Marshaler   = protoList[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder]{}
	_ json.Unmarshaler = &protoList[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder]{}
)

func (pl protoList[TMessage, TMessagePtr]) MarshalJSON() ([]byte, error) {
	b := []byte("[")
	for i, m := range pl {
		var err error
		b, err = protojson.MarshalOptions{}.MarshalAppend(b, TMessagePtr(m))
		if err != nil {
			return nil, err
		}
		if i != len(pl)-1 {
			b = append(b, ',')
		}
	}
	return append(b, ']'), nil
}

func (pl *protoList[TMessage, TMessagePtr]) UnmarshalJSON(b []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(b))
	t, err := decoder.Token()
	if err != nil {
		return err
	}
	if t != json.Delim('[') {
		return errors.New("expected start of list")
	}

	var values protoList[TMessage, TMessagePtr]
	for decoder.More() {
		var value protoUnmarshaler[TMessage, TMessagePtr]
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		values = append(values, &value.message)
	}
	*pl = values
	return nil
}

type protoUnmarshaler[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
] struct {
	message TMessage
}

var _ json.Unmarshaler = &protoUnmarshaler[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder]{}

func (pu *protoUnmarshaler[TMessage, TMessagePtr]) UnmarshalJSON(b []byte) error {
	return protojson.Unmarshal(b, TMessagePtr(&pu.message))
}

// payloadRenderer implements a strategy for rendering the contents of
// an object as HTML.
type payloadRenderer interface {
	queryParameter() string
	name() string
	// Render the object, returning the index of the encoder that
	// was used to decode it, or -1 if the object was not decoded.
	render(r *http.Request, o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]g.Node, int, []*browser_pb.RecentlyObservedEncoder)
}

// rawPayloadRenderer renders an object without performing any decoding
// steps. The output resembles that of "hexdump -C".
type rawPayloadRenderer struct{}

var _ payloadRenderer = rawPayloadRenderer{}

func (rawPayloadRenderer) queryParameter() string { return "raw" }
func (rawPayloadRenderer) name() string           { return "Raw" }

func (rawPayloadRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]g.Node, int, []*browser_pb.RecentlyObservedEncoder) {
	return []g.Node{
		h.Pre(g.Text(hex.Dump(o.Value.GetPayload()))),
	}, -1, nil
}

// The browser only decodes objects, using encoders that the user
// explicitly selected or recently observed. There is thus no need to
// enforce the use of encryption.
var browserBinaryEncoderFactory = model_encoding.NewBinaryEncoderFactory(true)

// mayTryEncoder returns whether an encoder at a given index in the
// list of recently observed encoders may be used to decode an object.
// The first encoder is always tried. Other encoders are only tried if
// they are auto-detecting, as encoders that are not may successfully
// decode objects that were encoded using a different configuration,
// yielding bogus results. Such encoders need to be selected explicitly.
func mayTryEncoder(index int, binaryEncoder model_encoding.BinaryDecoder) bool {
	return index == 0 || binaryEncoder.IsAutoDetecting()
}

func decodeObject(o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]byte, int, error) {
	for i, recentlyObservedEncoder := range recentlyObservedEncoders {
		objectReference := o.Value.GetLocalReference()
		binaryEncoder, err := browserBinaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(
			recentlyObservedEncoder.GetConfiguration(),
			uint32(objectReference.GetReferenceFormat().GetMaximumObjectSizeBytes()),
		)
		if err != nil {
			// The cookie may contain invalid encoders
			// that were stored before encoders were
			// validated. Ignore these.
			continue
		}
		if !mayTryEncoder(i, binaryEncoder) {
			continue
		}
		if decodedObject, err := binaryEncoder.DecodeBinary(o.Value.GetPayload(), o.GetDecodingParameters()); err == nil {
			return decodedObject, i, nil
		}
	}
	return nil, -1, errors.New("object can't be decoded using the selected encoder or any of the recently observed auto-detecting encoders")
}

// decodedPayloadRenderer renders the decoded payload of an object. The
// output resembles that of "hexdump -C".
type decodedPayloadRenderer struct{}

var _ payloadRenderer = decodedPayloadRenderer{}

func (decodedPayloadRenderer) queryParameter() string { return "decoded" }
func (decodedPayloadRenderer) name() string           { return "Decoded" }

func (decodedPayloadRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]g.Node, int, []*browser_pb.RecentlyObservedEncoder) {
	decodedObject, usedEncoderIndex, err := decodeObject(o, recentlyObservedEncoders)
	if err != nil {
		return renderErrorAlert(err), -1, nil
	}
	return []g.Node{
		h.Pre(g.Text(hex.Dump(decodedObject))),
	}, usedEncoderIndex, nil
}

// textPayloadRenderer renders the decoded payload of an object,
// assuming it is UTF-8 encoded text. Any non-UTF-8 byte sequences are
// replaced with U+FFFD "�". If the number of invalid characters is too
// high, output is suppressed.
type textPayloadRenderer struct{}

var _ payloadRenderer = textPayloadRenderer{}

func (textPayloadRenderer) queryParameter() string { return "text" }
func (textPayloadRenderer) name() string           { return "Text" }

func (textPayloadRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]g.Node, int, []*browser_pb.RecentlyObservedEncoder) {
	decodedObject, usedEncoderIndex, err := decodeObject(o, recentlyObservedEncoders)
	if err != nil {
		return renderErrorAlert(err), -1, nil
	}

	if utf8.Valid(decodedObject) {
		// Fast path: byte slice is already valid UTF-8.
		return renderTextWithLineNumbers(string(decodedObject)), usedEncoderIndex, nil
	}

	// Slow path: byte slice contains one or more invalid sequences.
	runeCount, badRuneCount := 0, 0
	var sb strings.Builder
	for {
		r, size := utf8.DecodeRune(decodedObject)
		if size == 0 {
			break
		}

		runeCount++
		if size == 1 && r == utf8.RuneError {
			badRuneCount++
		}

		sb.WriteRune(r)
		decodedObject = decodedObject[size:]
	}

	if badRuneCount > runeCount/10 {
		return renderErrorAlert(errors.New("object contains binary data, or the encoder configuration is incorrect")), usedEncoderIndex, nil
	}

	return renderTextWithLineNumbers(sb.String()), usedEncoderIndex, nil
}

// renderTextWithLineNumbers renders plain text, prefixed with line
// numbers. Line numbers are placed in a separate column, so that they
// are not included when copying the text. Each line number is a link
// to an anchor placed on the corresponding line of text, so that
// individual lines can be referenced and highlighted.
func renderTextWithLineNumbers(text string) []g.Node {
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	lineNumberNodes := make([]g.Node, 0, 2*len(lines)+1)
	lineNumberNodes = append(lineNumberNodes, h.Class("text-right text-gray-500 select-none"))
	lineNodes := make([]g.Node, 0, len(lines)+1)
	lineNodes = append(lineNodes, h.Class("text-lines"))
	for i, line := range lines {
		lineNumber := strconv.Itoa(i + 1)
		anchor := "L" + lineNumber
		lineNumberNodes = append(
			lineNumberNodes,
			h.A(h.Href("#"+anchor), g.Text(lineNumber)),
			g.Text("\n"),
		)
		lineNodes = append(
			lineNodes,
			h.Span(h.ID(anchor), g.Text(line)),
		)
	}

	return []g.Node{
		h.Div(
			h.Class("flex"),
			h.Pre(lineNumberNodes...),
			h.Pre(lineNodes...),
		),
	}
}

// messagePayloadRenderer renders the decoded payload of an object,
// assuming it is a Protobuf message. The message is either rendered as
// JSON, or pretty printed.
type messagePayloadRenderer struct {
	pretty bool
}

var _ payloadRenderer = messagePayloadRenderer{}

func payloadRendererQueryParameter(pretty bool) string {
	if pretty {
		return "pretty"
	}
	return "json"
}

func payloadRendererName(pretty bool) string {
	if pretty {
		return "Pretty"
	}
	return "JSON"
}

func (s messagePayloadRenderer) queryParameter() string {
	return payloadRendererQueryParameter(s.pretty)
}
func (s messagePayloadRenderer) name() string { return payloadRendererName(s.pretty) }

func (s messagePayloadRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]g.Node, int, []*browser_pb.RecentlyObservedEncoder) {
	decodedObject, usedEncoderIndex, err := decodeObject(o, recentlyObservedEncoders)
	if err != nil {
		return renderErrorAlert(err), -1, nil
	}

	messageTypeStr := r.PathValue("message_type")
	messageType, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(messageTypeStr))
	if err != nil {
		return renderErrorAlert(fmt.Errorf("invalid message type %#v: %w", messageTypeStr, err)), usedEncoderIndex, nil
	}

	message := messageType.New()
	if err := proto.Unmarshal(decodedObject, message.Interface()); err != nil {
		return renderErrorAlert(fmt.Errorf("failed to unmarshal message: %w", err)), usedEncoderIndex, nil
	}
	referenceFormat := o.Value.GetReferenceFormat()
	mr := newMessageRenderer("../..", &referenceFormat, s.pretty)
	rendered := mr.renderTopLevelMessage(model_core.NewTopLevelMessage(message, o.Value))
	return rendered, usedEncoderIndex, mr.observedEncoders
}

// messageListPayloadRenderer renders the decoded payload of an object,
// assuming it is a varint separated list of Protobuf messages. The
// messages are either rendered as JSON, or pretty printed.
type messageListPayloadRenderer struct {
	pretty bool
}

var _ payloadRenderer = messageListPayloadRenderer{}

func (s messageListPayloadRenderer) queryParameter() string {
	return payloadRendererQueryParameter(s.pretty)
}
func (s messageListPayloadRenderer) name() string { return payloadRendererName(s.pretty) }

func (s messageListPayloadRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]g.Node, int, []*browser_pb.RecentlyObservedEncoder) {
	decodedObject, usedEncoderIndex, err := decodeObject(o, recentlyObservedEncoders)
	if err != nil {
		return renderErrorAlert(err), -1, nil
	}

	messageTypeStr := r.PathValue("message_type")
	messageType, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(messageTypeStr))
	if err != nil {
		return renderErrorAlert(fmt.Errorf("invalid message type %#v: %w", messageTypeStr, err)), usedEncoderIndex, nil
	}

	var elements []protoreflect.Message
	originalDataLength := len(decodedObject)
	for len(decodedObject) > 0 {
		// Extract the size of the element.
		offset := originalDataLength - len(decodedObject)
		length, lengthLength := varint.ConsumeForward[uint](decodedObject)
		if lengthLength < 0 {
			return renderErrorAlert(fmt.Errorf("invalid element length at offset %d", offset)), usedEncoderIndex, nil
		}

		// Validate the size.
		decodedObject = decodedObject[lengthLength:]
		if length > uint(len(decodedObject)) {
			return renderErrorAlert(fmt.Errorf("length of element at offset %d is %d bytes, which exceeds maximum permitted size of %d bytes", offset, length, len(decodedObject))), usedEncoderIndex, nil
		}

		// Unmarshal the element.
		element := messageType.New()
		if err := proto.Unmarshal(decodedObject[:length], element.Interface()); err != nil {
			return renderErrorAlert(fmt.Errorf("failed to unmarshal element at offset %d: %w", offset, err)), usedEncoderIndex, nil
		}
		elements = append(elements, element)
		decodedObject = decodedObject[length:]
	}

	referenceFormat := o.Value.GetReferenceFormat()
	mr := newMessageRenderer("../..", &referenceFormat, s.pretty)
	rendered := mr.renderMessageList(model_core.NewMessage(elements, o.Value))
	return rendered, usedEncoderIndex, mr.observedEncoders
}
