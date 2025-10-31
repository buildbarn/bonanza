package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/sha256"
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

	g "maragu.dev/gomponents"
	c "maragu.dev/gomponents/components"
	h "maragu.dev/gomponents/html"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
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

func wrapHandler(handler func(http.ResponseWriter, *http.Request) (g.Node, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		node, err := handler(w, r)
		if err != nil {
			st := status.Convert(err)
			http.Error(w, err.Error(), http_server.StatusCodeFromGRPCCode(st.Code()))
			return
		}

		if err := node.Render(w); err != nil {
			http.Error(w, "error rendering node: "+err.Error(), http.StatusInternalServerError)
		}
	}
}

// RegisterHandlers registers handlers for serving web pages in a mux.
func (s *BrowserService) RegisterHandlers(mux *http.ServeMux) {
	// Storage related endpoints.
	mux.HandleFunc(
		"/evaluation/{instance_name}/{reference_format}/{reference}/{key}",
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

func renderPage(title string, body []g.Node) g.Node {
	return c.HTML5(c.HTML5Props{
		Title:    title,
		Language: "en",
		Head:     []g.Node{h.StyleEl(g.Raw(stylesheet))},
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
		object.NewInstanceName(r.PathValue("instance_name")).WithLocalReference(localReference.Value),
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
			&browser_pb.RecentlyObservedEncoder{
				Configuration: encoder.Configuration,
			},
		); err == nil {
			key := string(marshaled)
			if index, ok := seen[key]; ok {
				if existing := out[index]; encoder.LastObservation.GetTime().AsTime().
					After(existing.LastObservation.GetTime().AsTime()) {
					existing.LastObservation = encoder.LastObservation
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

func getEncoderFromForm(r *http.Request) ([]*browser_pb.RecentlyObservedEncoder, string, error) {
	if err := r.ParseForm(); err != nil {
		return nil, "", fmt.Errorf("failed to parse form: %w", err)
	}
	provided := r.FormValue("encoder_configuration")
	if provided == "" {
		return nil, "", nil
	}
	var unmarshaled protoList[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder]
	if err := json.Unmarshal([]byte(provided), &unmarshaled); err != nil {
		return nil, provided, fmt.Errorf("failed to unmarshal encoder configuration: %w", err)
	}
	return []*browser_pb.RecentlyObservedEncoder{{
		Configuration: unmarshaled,
	}}, "", nil
}

func getEncodersFromRequest(r *http.Request) (*browser_pb.Cookie, string, error) {
	recentlyObservedEncoders, currentEncoderConfigurationStr, err := getEncoderFromForm(r)

	// Restore any recently observed encoders from the cookie.
	cookie := getCookie(r)
	recentlyObservedEncoders = append(recentlyObservedEncoders, cookie.RecentlyObservedEncoders...)

	// Always provide an empty encoder. This guarantees that the
	// resulting list of encoders is non-empty.
	recentlyObservedEncoders = append(recentlyObservedEncoders, &browser_pb.RecentlyObservedEncoder{})

	// If no explicit configuration was provided through the form,
	// put the currently active configuration in the textarea, so
	// that it can easily be edited.
	recentlyObservedEncoders = trimRecentlyObservedEncoders(recentlyObservedEncoders)
	cookie.RecentlyObservedEncoders = recentlyObservedEncoders
	return cookie, currentEncoderConfigurationStr, err
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
		append(
			[]g.Node{
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
			},
		)...,
	)
}

func renderEncoderSelector(recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder, currentEncoderConfiguration string) []g.Node {
	if currentEncoderConfiguration == "" {
		if marshaled, err := json.MarshalIndent(
			protoList[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder](
				recentlyObservedEncoders[0].Configuration,
			),
			/* prefix = */ "",
			/* indent = */ "  ",
		); err == nil {
			currentEncoderConfiguration = string(marshaled)
		}
	}

	recentlyObservedEncodersNodes := []g.Node{
		h.Class("card bg-base-200 w-full p-4 shadow"),
		h.H1(
			h.Class("card-title text-2xl"),
			g.Text("Recently observed encoders"),
		),
	}
	for _, recentlyObservedEncoder := range recentlyObservedEncoders {
		l := protoList[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder](recentlyObservedEncoder.Configuration)
		compact, err := json.Marshal(l)
		if err != nil {
			continue
		}
		indented, err := json.MarshalIndent(l, "", "  ")
		if err != nil {
			continue
		}

		cardBody := []g.Node{
			h.Class("card bg-base-100 shadow-sm w-full mt-4 p-4 text-left overflow-x-hidden"),
		}
		if o := recentlyObservedEncoder.LastObservation; o != nil {
			cardBody = append(
				cardBody,
				h.H2(
					h.Class("text-xl"),
					g.Text(o.MessageType),
					g.Text("."),
					g.Text(o.FieldName),
				),
				h.P(
					h.Class("text-xs"),
					g.Text("Last observed: "),
					g.Text(o.Time.AsTime().Format(time.RFC3339)),
				),
			)
		} else {
			cardBody = append(
				cardBody,
				h.H2(
					h.Class("text-xl"),
					g.Text("User provided"),
				),
			)
		}
		cardBody = append(
			cardBody,
			h.Pre(
				h.Class("text-sm"),
				g.Text(string(indented)),
			),
			h.Div(
				h.Class("card-actions justify-end"),
				h.Button(
					h.Class("btn btn-primary"),
					g.Text("Use"),
				),
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
		h.Div(
			h.Class("card bg-base-200 w-full p-4 shadow"),
			h.H1(
				h.Class("card-title text-2xl mb-4"),
				g.Text("Current encoder configuration"),
			),

			h.Form(
				h.Method("post"),
				h.Textarea(
					h.Class("font-mono textarea w-full"),
					h.Name("encoder_configuration"),
					h.Rows("10"),
					h.Placeholder(`[
  {
    "lzwCompressing": {}
  },
  {
    "deterministicEncrypting": {
      "encryptionKey": "..."
    }
  }
]`),
					g.Text(currentEncoderConfiguration),
				),
				h.Div(
					h.Class("card-actions justify-end mt-4"),
					h.Button(
						h.Class("btn btn-primary"),
						g.Text("Update"),
					),
				),
			),
		),

		h.Div(recentlyObservedEncodersNodes...),
	}
}

// renderEvaluationPage renders a HTML page for displaying the contents
// of an evaluation of a key stored in an evaluation list.
func renderEvaluationPage(
	w http.ResponseWriter,
	evaluationListReference model_core.Decodable[object.GlobalReference],
	keyJSONNodes []g.Node,
	valueNodes []g.Node,
	dependenciesNodes []g.Node,
	currentEncoderConfiguration string,
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
		h.Div(append(
			[]g.Node{
				h.Class("card my-2 p-4 bg-neutral text-neutral-content font-mono h-auto! overflow-x-auto"),
			},
			keyJSONNodes...,
		)...),

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
					renderReferenceCard("Evaluation list reference", evaluationListReference),
				},
				renderEncoderSelector(cookie.RecentlyObservedEncoders, currentEncoderConfiguration)...,
			)...),

			h.Div(evaluationCard...),
		),
	})
}

func (s *BrowserService) doEvaluation(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	evaluationListReference, err := getReferenceFromRequest(r)
	if err != nil {
		return nil, err
	}
	referenceFormat := evaluationListReference.Value.GetReferenceFormat()

	keyBytes, err := base64.RawURLEncoding.DecodeString(r.PathValue("key"))
	if err != nil {
		return nil, err
	}
	keySHA256 := sha256.Sum256(keyBytes)
	keyAny, err := model_core.UnmarshalTopLevelMessage[anypb.Any](referenceFormat, keyBytes)
	if err != nil {
		return nil, err
	}

	jsonRenderer := messageJSONRenderer{
		basePath: path.Join(
			"../../../../object",
			url.PathEscape(evaluationListReference.Value.InstanceName.String()),
			referenceFormat.ToProto().String(),
		),
		referenceFormat: &referenceFormat,
		now:             time.Now(),
	}
	keyJSONNodes := jsonRenderer.renderTopLevelMessage(
		model_core.NewTopLevelMessage(keyAny.Message.ProtoReflect(), keyAny.OutgoingReferences),
	)

	cookie, currentEncoderConfigurationStr, err := getEncodersFromRequest(r)
	if err != nil {
		errNodes := renderErrorAlert(fmt.Errorf("failed to obtain encoder configuration: %w", err))
		return renderEvaluationPage(
			w,
			evaluationListReference,
			keyJSONNodes,
			errNodes,
			errNodes,
			currentEncoderConfigurationStr,
			cookie,
		), nil
	}

	binaryEncoder, err := model_encoding.NewBinaryEncoderFromProto(
		cookie.RecentlyObservedEncoders[0].Configuration,
		uint32(referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		errNodes := renderErrorAlert(fmt.Errorf("failed to create encoder: %w", err))
		return renderEvaluationPage(
			w,
			evaluationListReference,
			keyJSONNodes,
			errNodes,
			errNodes,
			currentEncoderConfigurationStr,
			cookie,
		), nil
	}

	parsedObjectPoolIngester := model_parser.NewParsedObjectPoolIngester(
		s.parsedObjectPool,
		model_parser.NewDownloadingParsedObjectReader(
			object_namespacemapping.NewNamespaceAddingDownloader(s.objectDownloader, evaluationListReference.Value.InstanceName),
		),
	)
	evaluationListReader := model_parser.LookupParsedObjectReader(
		parsedObjectPoolIngester,
		model_parser.NewChainedObjectParser(
			model_parser.NewEncodedObjectParser[object.LocalReference](binaryEncoder),
			model_parser.NewProtoListObjectParser[object.LocalReference, model_evaluation_pb.Evaluation](),
		),
	)
	ctx := r.Context()
	evaluationList, err := evaluationListReader.ReadParsedObject(
		ctx,
		model_core.CopyDecodable(evaluationListReference, evaluationListReference.Value.LocalReference),
	)
	if err != nil {
		errNodes := renderErrorAlert(fmt.Errorf("failed to download and decode evaluation list object: %w", err))
		return renderEvaluationPage(
			w,
			evaluationListReference,
			keyJSONNodes,
			errNodes,
			errNodes,
			currentEncoderConfigurationStr,
			cookie,
		), nil
	}

	evaluation, err := btree.Find(
		ctx,
		evaluationListReader,
		evaluationList,
		func(entry model_core.Message[*model_evaluation_pb.Evaluation, object.LocalReference]) (int, *model_core_pb.DecodableReference) {
			switch level := entry.Message.Level.(type) {
			case *model_evaluation_pb.Evaluation_Leaf_:
				flattenedKey, err := model_core.FlattenAny(model_core.Nested(entry, level.Leaf.Key))
				if err != nil {
					return -1, nil
				}
				marshaledKey, err := model_core.MarshalTopLevelMessage(flattenedKey)
				if err != nil {
					return -1, nil
				}
				marshaledKeySHA256 := sha256.Sum256(marshaledKey)
				return bytes.Compare(keySHA256[:], marshaledKeySHA256[:]), nil
			case *model_evaluation_pb.Evaluation_Parent_:
				return bytes.Compare(keySHA256[:], level.Parent.FirstKeySha256), level.Parent.Reference
			default:
				return 0, nil
			}
		},
	)
	if err != nil {
		errNodes := renderErrorAlert(fmt.Errorf("failed to look up key in evaluation list: %w", err))
		return renderEvaluationPage(
			w,
			evaluationListReference,
			keyJSONNodes,
			errNodes,
			errNodes,
			currentEncoderConfigurationStr,
			cookie,
		), nil
	}

	valueNodes := renderWarningAlert("This key yields a native value that cannot be represented as JSON, or evaluation failed to yield a value.")
	dependenciesNodes := renderWarningAlert("This key has no dependencies, or evaluation failed to yield a list of dependencies.")
	if evaluation.IsSet() {
		evaluationLeaf, ok := evaluation.Message.Level.(*model_evaluation_pb.Evaluation_Leaf_)
		if !ok {
			errNodes := renderErrorAlert(errors.New("evaluation list entry is not a valid leaf"))
			return renderEvaluationPage(
				w,
				evaluationListReference,
				keyJSONNodes,
				errNodes,
				errNodes,
				currentEncoderConfigurationStr,
				cookie,
			), nil
		}

		if v := evaluationLeaf.Leaf.Value; v != nil {
			valueNodes = []g.Node{
				h.Div(
					append(
						[]g.Node{
							h.Class("card my-2 p-4 bg-neutral text-neutral-content font-mono h-auto! overflow-x-auto"),
						},
						jsonRenderer.renderMessage(model_core.Nested(evaluation, v.ProtoReflect()))...,
					)...,
				),
			}
		}

		if dependencies := evaluationLeaf.Leaf.Dependencies; len(dependencies) > 0 {
			dependencyListReader := model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					model_parser.NewEncodedObjectParser[object.LocalReference](binaryEncoder),
					model_parser.NewProtoListObjectParser[object.LocalReference, model_evaluation_pb.Keys](),
				),
			)
			var errIter error
			var nodes []g.Node
			for dependency := range btree.AllLeaves(
				ctx,
				dependencyListReader,
				model_core.Nested(evaluation, dependencies),
				func(element model_core.Message[*model_evaluation_pb.Keys, object.LocalReference]) (*model_core_pb.DecodableReference, error) {
					return element.Message.GetParent().GetReference(), nil
				},
				&errIter,
			) {
				if dependencyLeaf, ok := dependency.Message.Level.(*model_evaluation_pb.Keys_Leaf); ok {
					cardNodes := []g.Node{
						h.Class("block card my-2 p-4 bg-neutral text-neutral-content font-mono h-auto! overflow-x-auto"),
					}
					if dependencyKey, err := model_core.FlattenAny(model_core.Nested(dependency, dependencyLeaf.Leaf)); err == nil {
						if marshaledDependencyKey, err := model_core.MarshalTopLevelMessage(dependencyKey); err == nil {
							cardNodes = append(cardNodes, h.Form(
								h.Action(base64.RawURLEncoding.EncodeToString(marshaledDependencyKey)),
								h.Button(
									h.Class("btn btn-primary btn-square float-right inline-block"),
									g.Text("↗"),
								),
							))
						}
					}
					cardNodes = append(
						cardNodes,
						jsonRenderer.renderMessage(model_core.Nested(dependency, dependencyLeaf.Leaf.ProtoReflect()))...,
					)
					nodes = append(nodes, h.Div(cardNodes...))
				}
			}
			if errIter == nil {
				dependenciesNodes = nodes
			} else {
				dependenciesNodes = renderErrorAlert(errIter)
			}
		}
	}

	// Rendering values might reveal the existence of additional encoders.
	cookie.RecentlyObservedEncoders = trimRecentlyObservedEncoders(
		append(
			append(
				[]*browser_pb.RecentlyObservedEncoder{cookie.RecentlyObservedEncoders[0]},
				jsonRenderer.observedEncoders...,
			),
			cookie.RecentlyObservedEncoders[1:]...,
		),
	)
	return renderEvaluationPage(
		w,
		evaluationListReference,
		keyJSONNodes,
		valueNodes,
		dependenciesNodes,
		currentEncoderConfigurationStr,
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
	currentEncoderConfiguration string,
	cookie *browser_pb.Cookie,
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
	return renderPage(rawReference, []g.Node{
		h.Div(
			h.Class("flex w-full space-x-4 p-4"),

			h.Div(append(
				[]g.Node{
					h.Class("flex flex-col w-1/3 space-y-4"),
					renderReferenceCard("Object reference", decodableReference),
				},
				renderEncoderSelector(cookie.RecentlyObservedEncoders, currentEncoderConfiguration)...,
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

	cookie, currentEncoderConfigurationStr, err := getEncodersFromRequest(r)
	if err != nil {
		return renderObjectPage(
			w,
			objectReference,
			payloadRenderers,
			currentPayloadRendererIndex,
			currentEncoderConfigurationStr,
			cookie,
			renderErrorAlert(fmt.Errorf("failed to obtain encoder configuration: %w", err)),
		), nil
	}

	// Fetch and render the object.
	o, err := s.objectDownloader.DownloadObject(r.Context(), objectReference.Value)
	if err != nil {
		return renderObjectPage(
			w,
			objectReference,
			payloadRenderers,
			currentPayloadRendererIndex,
			currentEncoderConfigurationStr,
			cookie,
			renderErrorAlert(fmt.Errorf("failed to download object: %w", err)),
		), nil
	}
	rendered, usedEncoderIndex, encodersInObject := payloadRenderers[currentPayloadRendererIndex].render(
		r,
		model_core.CopyDecodable(objectReference, o),
		cookie.RecentlyObservedEncoders,
	)

	// For future page views, change the order of encoders to:
	// - The encoder that was used to decrypt the object.
	// - Any encoders found in the object that is currently being
	//   displayed.
	// - All existing encoders that were not used this time.
	cookie.RecentlyObservedEncoders = trimRecentlyObservedEncoders(
		append(
			append(
				append(
					[]*browser_pb.RecentlyObservedEncoder{cookie.RecentlyObservedEncoders[usedEncoderIndex]},
					encodersInObject...,
				),
				cookie.RecentlyObservedEncoders[:usedEncoderIndex]...,
			),
			cookie.RecentlyObservedEncoders[usedEncoderIndex+1:]...,
		),
	)

	return renderObjectPage(
		w,
		objectReference,
		payloadRenderers,
		currentPayloadRendererIndex,
		currentEncoderConfigurationStr,
		cookie,
		rendered,
	), nil
}

func (s *BrowserService) doProtoObject(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	return s.doObject(
		w,
		r,
		[]payloadRenderer{
			rawPayloadRenderer{},
			decodedPayloadRenderer{},
			messageJSONPayloadRenderer{
				CustomRenderer: stubCustomRenderer,
			},
			messagePrettyRenderer{
				jsonRenderer: messageJSONPayloadRenderer{
					CustomRenderer: renderMessagePretty,
				},
			},
		},
		2,
	)
}

func (s *BrowserService) doProtoListObject(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	return s.doObject(
		w,
		r,
		[]payloadRenderer{
			rawPayloadRenderer{},
			decodedPayloadRenderer{},
			messageListJSONPayloadRenderer{},
		},
		2,
	)
}

func (s *BrowserService) doRawObject(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	return s.doObject(
		w,
		r,
		[]payloadRenderer{
			rawPayloadRenderer{},
			decodedPayloadRenderer{},
			textPayloadRenderer{},
		},
		2,
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
	tableRows := make([]g.Node, 0, len(response.PlatformQueues))
	for _, pq := range response.PlatformQueues {
		for i, scq := range pq.SizeClassQueues {
			// For each platform queue, add a multi-row
			// column on the left hand side containing the
			// PKIX public keys.
			var cells []g.Node
			if i == 0 {
				pkixPublicKeys := append(
					make([]g.Node, 0, 2+2*len(pq.PkixPublicKeys)),
					h.Class("break-all font-mono"),
					h.RowSpan(strconv.FormatInt(int64(len(pq.SizeClassQueues)), 10)),
				)
				for _, pkixPublicKey := range pq.PkixPublicKeys {
					pkixPublicKeys = append(
						pkixPublicKeys,
						g.Text(base64.StdEncoding.EncodeToString(pkixPublicKey)),
						h.Br(),
					)
				}
				cells = append(cells, h.Td(pkixPublicKeys...))
			}

			sizeClassQueueName := &buildqueuestate_pb.SizeClassQueueName{
				PlatformPkixPublicKey: pq.PkixPublicKeys[0],
				SizeClass:             scq.SizeClass,
			}
			sizeClassQueueNameStr, err := marshalAndURLEncode(sizeClassQueueName)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal and encode size class queue name: %w", err)
			}
			invocationName := &buildqueuestate_pb.InvocationName{
				SizeClassQueueName: sizeClassQueueName,
			}
			invocationNameStr, err := marshalAndURLEncode(invocationName)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal and encode invocation name: %w", err)
			}
			listWorkersFilterExecutingStr, err := marshalAndURLEncode(&buildqueuestate_pb.ListWorkersRequest_Filter{
				Type: &buildqueuestate_pb.ListWorkersRequest_Filter_Executing{
					Executing: invocationName,
				},
			})
			if err != nil {
				return nil, fmt.Errorf("failed to marshal and encode executing workers filter: %w", err)
			}
			listWorkersFilterIdleSynchronizingStr, err := marshalAndURLEncode(&buildqueuestate_pb.ListWorkersRequest_Filter{
				Type: &buildqueuestate_pb.ListWorkersRequest_Filter_IdleSynchronizing{
					IdleSynchronizing: invocationName,
				},
			})
			if err != nil {
				return nil, fmt.Errorf("failed to marshal and encode idle synchronizing workers filter: %w", err)
			}
			listWorkersFilterAllStr, err := marshalAndURLEncode(&buildqueuestate_pb.ListWorkersRequest_Filter{
				Type: &buildqueuestate_pb.ListWorkersRequest_Filter_All{
					All: sizeClassQueueName,
				},
			})
			if err != nil {
				return nil, fmt.Errorf("failed to marshal and encode all workers filter: %w", err)
			}

			// Counters that are tracked per size class queue.
			tableRows = append(
				tableRows,
				h.Tr(
					append(
						cells,
						h.Td(h.Class("text-right"), g.Textf("%d", scq.SizeClass)),
						h.Td(h.Class("text-right"), g.Text(timeoutToText(scq.Timeout, now))),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("queued_operations/"+invocationNameStr),
								g.Textf("%d", scq.RootInvocation.QueuedOperationsCount),
							),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("invocation_children/"+invocationNameStr+"/QUEUED"),
								g.Textf("%d", scq.RootInvocation.QueuedChildrenCount),
							),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("invocation_children/"+invocationNameStr+"/ACTIVE"),
								g.Textf("%d", scq.RootInvocation.ActiveChildrenCount),
							),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("invocation_children/"+invocationNameStr+"/ALL"),
								g.Textf("%d", scq.RootInvocation.ChildrenCount),
							),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("workers/"+listWorkersFilterExecutingStr),
								g.Textf("%d", scq.RootInvocation.ExecutingWorkersCount),
							),
						),
						h.Td(
							h.Class("text-right"),
							g.Textf("%d", scq.RootInvocation.IdleWorkersCount),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("workers/"+listWorkersFilterIdleSynchronizingStr),
								g.Textf("%d", scq.RootInvocation.IdleSynchronizingWorkersCount),
							),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("workers/"+listWorkersFilterAllStr),
								g.Textf("%d", scq.WorkersCount),
							),
						),
						h.Td(
							h.Class("text-right"),
							h.A(
								h.Class("link link-primary"),
								h.Href("drains/"+sizeClassQueueNameStr),
								g.Textf("%d", scq.DrainsCount),
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
								g.Textf("%d", operationsCount.PaginationInfo.GetTotalEntries()),
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
	response, err := s.buildQueueStateClient.GetOperation(r.Context(), &buildqueuestate_pb.GetOperationRequest{
		OperationName: operationName,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get operation: %w", err)
	}
	operation := response.Operation

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
		cookie.EcdhPrivateKeys = append([][]byte{marshaledPrivateKey}, cookie.EcdhPrivateKeys...)
		if maximumCount := 10; len(cookie.EcdhPrivateKeys) > maximumCount {
			cookie.EcdhPrivateKeys = cookie.EcdhPrivateKeys[:maximumCount]
		}
	}

	action := operation.GetAction()
	if action == nil {
		return nil, errors.New("no action provided")
	}
	platformECDHPublicKey, err := crypto.ParsePKIXECDHPublicKey(action.PlatformPkixPublicKey)
	if err != nil {
		return nil, fmt.Errorf("action contains an invalid platform PKIX public key: %w", err)
	}
	clientCertificateChain := action.ClientCertificateChain
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
	for _, rawECDHPrivateKey := range cookie.EcdhPrivateKeys {
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
		jsonRenderer := messageJSONRenderer{
			now: now,
		}
		var executeWithStorageAction model_executewithstorage_pb.Action
		if actionMessage.UnmarshalTo(&executeWithStorageAction) == nil {
			namespace, err := object.NewNamespace(executeWithStorageAction.Namespace)
			if err != nil {
				return nil, fmt.Errorf("invalid namespace: %#v", err)
			}
			jsonRenderer.basePath = path.Join(
				"../../object",
				namespace.InstanceName.String(),
				namespace.ReferenceFormat.ToProto().String(),
			)
			jsonRenderer.fallbackObjectFormat = executeWithStorageAction.ActionFormat
			jsonRenderer.referenceFormat = &namespace.ReferenceFormat
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
		cookie.RecentlyObservedEncoders = trimRecentlyObservedEncoders(
			append(
				append(
					[]*browser_pb.RecentlyObservedEncoder(nil),
					jsonRenderer.observedEncoders...,
				),
				cookie.RecentlyObservedEncoders...,
			),
		)
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
							h.Td(g.Text(timeoutToText(operation.Timeout, now))),
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
	response, err := s.buildQueueStateClient.ListOperations(ctx, &buildqueuestate_pb.ListOperationsRequest{
		FilterInvocationId: filterInvocationID,
		FilterStage:        filterStage,
		PageSize:           pageSize,
		StartAfter:         startAfter,
	})
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
	paginationInfo := response.PaginationInfo
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
		g.Textf("Showing operations [%d, %d) of %d in total", startIndex, startIndex+uint32(len(response.Operations)), paginationInfo.GetTotalEntries()),
	))
	if l := len(response.Operations); l > 0 {
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

	tableRows := make([]g.Node, 0, len(response.Operations))
	now := time.Now()
	for _, operation := range response.Operations {
		tableRows = append(
			tableRows,
			h.Tr(
				h.Td(
					h.Class("font-mono"),
					h.A(
						h.Class("link link-primary"),
						h.Href("../../operation/"+operation.Name),
						g.Text(operation.Name),
					),
				),
				h.Td(h.Class("text-right"), g.Text(timeoutToText(operation.Timeout, now))),
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
	response, err := s.buildQueueStateClient.ListWorkers(r.Context(), &buildqueuestate_pb.ListWorkersRequest{
		Filter:   &filter,
		PageSize: pageSize,
		// StartAfter: startAfter,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list workers: %w", err)
	}

	tableRows := make([]g.Node, 0, len(response.Workers))
	for _, worker := range response.Workers {
		workerID := make([]g.Node, 0, 2*len(worker.Id))
		for _, key := range slices.Sorted(maps.Keys(worker.Id)) {
			workerID = append(
				workerID,
				h.Span(
					h.Class("badge badge-primary text-nowrap"),
					g.Textf("%s=%#v", key, worker.Id[key]),
				),
				g.Text(" "),
			)
		}
		cells := []g.Node{
			h.Td(workerID...),
			h.Td(h.Class("text-right"), g.Text(timeoutToText(worker.Timeout, now))),
		}
		if operation := worker.CurrentOperation; operation != nil {
			cells = append(
				cells,
				h.Td(
					h.Class("font-mono"),
					h.A(
						h.Class("link link-primary"),
						h.Href("../operation/"+operation.Name),
						g.Text(operation.Name),
					),
				),
				h.Td(h.Class("text-right"), g.Text(timeoutToText(operation.Timeout, now))),
			)
		} else {
			cellText := "idle"
			if worker.Drained {
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
	}, 0, nil
}

func decodeObject(o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]byte, int, error) {
	for i, recentlyObservedEncoder := range recentlyObservedEncoders {
		objectReference := o.Value.GetLocalReference()
		binaryEncoder, err := model_encoding.NewBinaryEncoderFromProto(
			recentlyObservedEncoder.Configuration,
			uint32(objectReference.GetReferenceFormat().GetMaximumObjectSizeBytes()),
		)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create encoder: %w", err)
		}
		if decodedObject, err := binaryEncoder.DecodeBinary(o.Value.GetPayload(), o.GetDecodingParameters()); err == nil {
			return decodedObject, i, nil
		}
	}
	return nil, 0, errors.New("object can't be decoded using any of the recently observed encoders")
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
		return renderErrorAlert(err), 0, nil
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
		return renderErrorAlert(err), 0, nil
	}

	if utf8.Valid(decodedObject) {
		// Fast path: byte slice is already valid UTF-8.
		return []g.Node{h.Pre(g.Text(string(decodedObject)))}, usedEncoderIndex, nil
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

	return []g.Node{h.Pre(g.Text(sb.String()))}, usedEncoderIndex, nil
}

// messageJSONRenderer is capable of rendering Protobuf messages as JSON
// with syntax highlighting applied. Any references to other objects
// contained in these messages are rendered as clickable links.
type messageJSONRenderer struct {
	basePath             string
	fallbackObjectFormat *model_core_pb.ObjectFormat
	referenceFormat      *object.ReferenceFormat
	now                  time.Time
	customRenderer       JSONCustomRenderer

	observedEncoders []*browser_pb.RecentlyObservedEncoder
}

// A function that can provide custom rendering for a particular message. If it
// is set and returns a nonempty slice, the returned rendering is used.
type JSONCustomRenderer func(d *messageJSONRenderer, m model_core.Message[protoreflect.Message, object.LocalReference], fields map[string][]g.Node) []g.Node

func stubCustomRenderer(d *messageJSONRenderer, m model_core.Message[protoreflect.Message, object.LocalReference], fields map[string][]g.Node) []g.Node {
	return nil
}

func (d *messageJSONRenderer) renderReferenceField(fieldDescriptor protoreflect.FieldDescriptor, reference model_core.Decodable[object.LocalReference]) []g.Node {
	fieldOptions := fieldDescriptor.Options().(*descriptorpb.FieldOptions)
	objectFormat := proto.GetExtension(fieldOptions, model_core_pb.E_ObjectFormat).(*model_core_pb.ObjectFormat)
	if objectFormat == nil {
		objectFormat = d.fallbackObjectFormat
	}

	rawReference := model_core.DecodableLocalReferenceToString(reference)
	if objectFormat == nil {
		// Field is a valid reference, but there is no type
		// information. Just show the reference without turning
		// it into a link.
		return []g.Node{
			h.Span(
				h.Class("whitespace-nowrap"),
				g.Textf("%#v", rawReference),
			),
		}
	}

	// Field is a valid reference for which we have type information
	// in the field options. Emit a link to the object.
	var link string
	switch format := objectFormat.GetFormat().(type) {
	case *model_core_pb.ObjectFormat_Raw:
		link = path.Join(d.basePath, rawReference, "raw")
	case *model_core_pb.ObjectFormat_ProtoTypeName:
		link = path.Join(d.basePath, rawReference, "proto", format.ProtoTypeName)
	case *model_core_pb.ObjectFormat_ProtoListTypeName:
		link = path.Join(d.basePath, rawReference, "proto_list", format.ProtoListTypeName)
	default:
		return []g.Node{
			h.Span(
				h.Class("text-red-600"),
				g.Text("[ Reference field with unknown object format type ]"),
			),
		}
	}
	return []g.Node{
		h.A(
			h.Class("link link-accent whitespace-nowrap"),
			h.Href(link),
			g.Textf("%#v", rawReference),
		),
	}
}

func (d *messageJSONRenderer) renderField(fieldDescriptor protoreflect.FieldDescriptor, value model_core.Message[protoreflect.Value, object.LocalReference]) []g.Node {
	var v any
	switch fieldDescriptor.Kind() {
	// Simple scalar types for which we can just call json.Marshal().
	case protoreflect.BoolKind:
		v = value.Message.Bool()
	case protoreflect.Int32Kind, protoreflect.Int64Kind,
		protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
		v = value.Message.Int()
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind,
		protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		v = value.Message.Uint()
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		v = value.Message.Float()
	case protoreflect.StringKind:
		v = value.Message.String()
	case protoreflect.BytesKind:
		v = value.Message.Bytes()

	case protoreflect.GroupKind, protoreflect.MessageKind:
		if d.basePath != "" {
			switch r := value.Message.Message().Interface().(type) {
			case *model_core_pb.DecodableReference:
				if reference, err := model_core.FlattenDecodableReference(model_core.Nested(value, r)); err == nil {
					return d.renderReferenceField(fieldDescriptor, reference)
				}
			case *model_core_pb.WeakDecodableReference:
				if d.referenceFormat != nil {
					if reference, err := model_core.NewDecodableLocalReferenceFromWeakProto(*d.referenceFormat, r); err == nil {
						return d.renderReferenceField(fieldDescriptor, reference)
					}
				}
			}
		}

		// Recurse into message.
		return d.renderMessage(model_core.Nested(value, value.Message.Message()))

	case protoreflect.EnumKind:
		// Render an enum value as a string or integer,
		// depending on whether it corresponds to a known value.
		number := value.Message.Enum()
		if enumValueDescriptor := fieldDescriptor.Enum().Values().ByNumber(number); enumValueDescriptor != nil {
			v = string(enumValueDescriptor.Name())
		} else {
			v = number
		}

	default:
		return []g.Node{
			h.Span(
				h.Class("text-red-600"),
				g.Text("[ Unknown field kind ]"),
			),
		}
	}

	return renderJSONValue(v)
}

func renderJSONValue(v any) []g.Node {
	jsonValue, err := json.Marshal(v)
	if err != nil {
		return []g.Node{
			h.Span(
				h.Class("text-red-600"),
				g.Textf("[ %s ]", err),
			),
		}
	}
	return []g.Node{
		h.Span(
			h.Class("text-fuchsia-300"),
			g.Text(string(jsonValue)),
		),
	}
}

var binaryEncoderDescriptor = (&model_encoding_pb.BinaryEncoder{}).ProtoReflect().Descriptor()

func (d *messageJSONRenderer) renderTopLevelMessage(m model_core.TopLevelMessage[protoreflect.Message, object.LocalReference]) []g.Node {
	switch message := m.Message.Interface().(type) {
	case *anypb.Any:
		anyValue, err := model_core.UnmarshalTopLevelAnyNew(model_core.NewTopLevelMessage(message, m.OutgoingReferences))
		if err != nil {
			return []g.Node{
				h.Span(
					h.Class("text-red-600"),
					g.Textf("[ %s ]", err),
				),
			}
		}
		return d.renderMessageCommon(
			model_core.Nested(anyValue.Decay(), anyValue.Message.ProtoReflect()),
			map[string][]g.Node{
				"@type": renderJSONValue(message.TypeUrl),
			},
		)
	case *model_core_pb.Any:
		// If the provided message is a model_core_pb.Any,
		// render it similar to how protojson renders an
		// anypb.Any. Namely, render the payload message with an
		// added "@type" field containing the type URL.
		anyValue, err := model_core.UnmarshalAnyNew(model_core.Nested(m.Decay(), message))
		if err != nil {
			return []g.Node{
				h.Span(
					h.Class("text-red-600"),
					g.Textf("[ %s ]", err),
				),
			}
		}
		return d.renderMessageCommon(
			model_core.Nested(anyValue.Decay(), anyValue.Message.ProtoReflect()),
			map[string][]g.Node{
				"@type": renderJSONValue(message.Value.TypeUrl),
			},
		)
	default:
		return d.renderMessageCommon(m.Decay(), map[string][]g.Node{})
	}
}

func (d *messageJSONRenderer) renderMessage(m model_core.Message[protoreflect.Message, object.LocalReference]) []g.Node {
	switch message := m.Message.Interface().(type) {
	case *model_core_pb.Any:
		// If the provided message is a model_core_pb.Any,
		// render it similar to how protojson renders an
		// anypb.Any. Namely, render the payload message with an
		// added "@type" field containing the type URL.
		anyValue, err := model_core.UnmarshalAnyNew(model_core.Nested(m, message))
		if err != nil {
			return []g.Node{
				h.Span(
					h.Class("text-red-600"),
					g.Textf("[ %s ]", err),
				),
			}
		}
		return d.renderMessageCommon(
			model_core.Nested(anyValue.Decay(), anyValue.Message.ProtoReflect()),
			map[string][]g.Node{
				"@type": renderJSONValue(message.Value.TypeUrl),
			},
		)
	default:
		return d.renderMessageCommon(m, map[string][]g.Node{})
	}
}

func (d *messageJSONRenderer) renderMessageCommon(m model_core.Message[protoreflect.Message, object.LocalReference], fields map[string][]g.Node) []g.Node {
	if r := d.customRenderer(d, m, fields); len(r) > 0 {
		return r
	}

	switch v := m.Message.Interface().(type) {
	case *durationpb.Duration:
		if jsonValue, err := protojson.Marshal(v); err == nil {
			return []g.Node{
				h.Span(
					h.Class("text-fuchsia-300"),
					g.Text(string(jsonValue)),
				),
			}
		}
	}

	// Iterate over all message fields and render their values.
	m.Message.Range(func(fieldDescriptor protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		var valueNodes []g.Node
		if fieldDescriptor.IsList() {
			// Repeated fields should be rendered as JSON lists.
			list := value.List()
			listLength := list.Len()
			if listLength == 0 {
				valueNodes = []g.Node{
					g.Text("[]"),
				}
			} else {
				listParts := make([]g.Node, 0, listLength)
				for i := 0; i < listLength; i++ {
					elementNodes := d.renderField(fieldDescriptor, model_core.Nested(m, list.Get(i)))
					if i != listLength-1 {
						elementNodes = append(elementNodes, g.Text(","))
					}
					listParts = append(listParts, h.Li(elementNodes...))
				}
				valueNodes = []g.Node{
					g.Text("["),
					h.Ul(listParts...),
					g.Text("]"),
				}
			}

			if fieldDescriptor.Message() == binaryEncoderDescriptor {
				configuration := make([]*model_encoding_pb.BinaryEncoder, 0, listLength)
				for i := 0; i < listLength; i++ {
					configuration = append(
						configuration,
						list.Get(i).Message().Interface().(*model_encoding_pb.BinaryEncoder),
					)
				}
				d.observedEncoders = append(
					d.observedEncoders,
					&browser_pb.RecentlyObservedEncoder{
						Configuration: configuration,
						LastObservation: &browser_pb.RecentlyObservedEncoder_LastObservation{
							Time:        timestamppb.New(d.now),
							MessageType: string(m.Message.Descriptor().Name()),
							FieldName:   fieldDescriptor.TextName(),
						},
					},
				)
			}
		} else if fieldDescriptor.IsMap() {
			mapField := value.Map()
			mapLength := mapField.Len()
			if mapLength == 0 {
				valueNodes = []g.Node{
					g.Text("{}"),
				}
			} else {
				mapParts := make([]g.Node, 0, len(fields))
				mapField.Range(func(entryKey protoreflect.MapKey, entryValue protoreflect.Value) bool {
					entryNodes := append(
						append(
							d.renderField(fieldDescriptor.MapKey(), model_core.Nested(m, entryKey.Value())),
							g.Text(": "),
						),
						d.renderField(fieldDescriptor.MapValue(), model_core.Nested(m, entryValue))...,
					)
					if len(mapParts) != mapLength-1 {
						entryNodes = append(entryNodes, g.Text(","))
					}
					mapParts = append(mapParts, h.Li(entryNodes...))
					return true
				})
				valueNodes = []g.Node{
					g.Text("{"),
					h.Ul(mapParts...),
					g.Text("}"),
				}
			}
		} else {
			valueNodes = d.renderField(fieldDescriptor, model_core.Nested(m, value))
		}
		name := fieldDescriptor.JSONName()
		fields[name] = valueNodes
		return true
	})

	// Sort fields by name and join them together in a single JSON object.
	if len(fields) == 0 {
		return []g.Node{g.Text("{}")}
	}

	sortedFields := make([]g.Node, 0, len(fields))
	for i, name := range slices.Sorted(maps.Keys(fields)) {
		entryNodes := []g.Node{
			h.Span(
				h.Class("text-amber-200"),
				g.Textf("%#v", name),
			),
			g.Text(": "),
		}
		entryNodes = append(entryNodes, fields[name]...)
		if i != len(fields)-1 {
			entryNodes = append(entryNodes, g.Text(","))
		}
		sortedFields = append(sortedFields, h.Li(entryNodes...))
	}

	return []g.Node{
		g.Text("{"),
		h.Ul(sortedFields...),
		g.Text("}"),
	}
}

func (d *messageJSONRenderer) renderMessageList(list model_core.Message[[]protoreflect.Message, object.LocalReference]) []g.Node {
	listLength := len(list.Message)
	if listLength == 0 {
		return []g.Node{
			g.Text("[]"),
		}
	}

	listParts := make([]g.Node, 0, listLength)
	for i, element := range list.Message {
		elementNodes := d.renderMessage(model_core.Nested(list, element))
		if i != listLength-1 {
			elementNodes = append(elementNodes, g.Text(","))
		}
		listParts = append(listParts, h.Li(elementNodes...))
	}

	return []g.Node{
		g.Text("["),
		h.Ul(listParts...),
		g.Text("]"),
	}
}

// messageJSONPayloadRenderer renders the decoded payload of an object,
// assuming it is a Protobuf message that can be converted to JSON.
type messageJSONPayloadRenderer struct {
	CustomRenderer JSONCustomRenderer
}

var _ payloadRenderer = messageJSONPayloadRenderer{}

func (messageJSONPayloadRenderer) queryParameter() string { return "json" }
func (messageJSONPayloadRenderer) name() string           { return "JSON" }

func (s messageJSONPayloadRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]g.Node, int, []*browser_pb.RecentlyObservedEncoder) {
	decodedObject, usedEncoderIndex, err := decodeObject(o, recentlyObservedEncoders)
	if err != nil {
		return renderErrorAlert(err), 0, nil
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
	d := messageJSONRenderer{
		basePath:        "../..",
		referenceFormat: &referenceFormat,
		customRenderer:  s.CustomRenderer,
		now:             time.Now(),
	}
	rendered := d.renderTopLevelMessage(model_core.NewTopLevelMessage(message, o.Value))
	return rendered, usedEncoderIndex, d.observedEncoders
}

// messageListJSONPayloadRenderer renders the decoded payload of an
// object, assuming it is a varint separated list of Protobuf messages
// that can be converted to JSON.
type messageListJSONPayloadRenderer struct{}

var _ payloadRenderer = messageListJSONPayloadRenderer{}

func (messageListJSONPayloadRenderer) queryParameter() string { return "json" }
func (messageListJSONPayloadRenderer) name() string           { return "JSON" }

func (messageListJSONPayloadRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]g.Node, int, []*browser_pb.RecentlyObservedEncoder) {
	decodedObject, usedEncoderIndex, err := decodeObject(o, recentlyObservedEncoders)
	if err != nil {
		return renderErrorAlert(err), 0, nil
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
	d := messageJSONRenderer{
		basePath:        "../..",
		referenceFormat: &referenceFormat,
		now:             time.Now(),
	}
	rendered := d.renderMessageList(model_core.NewMessage(elements, o.Value))
	return rendered, usedEncoderIndex, d.observedEncoders
}
