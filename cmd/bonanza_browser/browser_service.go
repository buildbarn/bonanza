package main

import (
	"bytes"
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
	"strings"
	"time"
	"unicode/utf8"

	bb_http "github.com/buildbarn/bb-storage/pkg/http"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/buildbarn/bonanza/pkg/encoding/varint"
	model_core "github.com/buildbarn/bonanza/pkg/model/core"
	"github.com/buildbarn/bonanza/pkg/model/core/btree"
	model_encoding "github.com/buildbarn/bonanza/pkg/model/encoding"
	model_parser "github.com/buildbarn/bonanza/pkg/model/parser"
	browser_pb "github.com/buildbarn/bonanza/pkg/proto/browser"
	model_core_pb "github.com/buildbarn/bonanza/pkg/proto/model/core"
	model_encoding_pb "github.com/buildbarn/bonanza/pkg/proto/model/encoding"
	model_evaluation_pb "github.com/buildbarn/bonanza/pkg/proto/model/evaluation"
	object_pb "github.com/buildbarn/bonanza/pkg/proto/storage/object"
	"github.com/buildbarn/bonanza/pkg/storage/object"
	object_namespacemapping "github.com/buildbarn/bonanza/pkg/storage/object/namespacemapping"

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
	"google.golang.org/protobuf/types/known/timestamppb"
)

//go:embed stylesheet.css
var stylesheet string

// BrowserService is capable of serving pages for inspecting the
// contents of objects in storage.
type BrowserService struct {
	objectDownloader object.Downloader[object.GlobalReference]
	parsedObjectPool *model_parser.ParsedObjectPool
}

// NewBrowserService creates a new BrowserService that serves pages,
// displaying the contents contained in a given storage backend.
func NewBrowserService(objectDownloader object.Downloader[object.GlobalReference], parsedObjectPool *model_parser.ParsedObjectPool) *BrowserService {
	return &BrowserService{
		objectDownloader: objectDownloader,
		parsedObjectPool: parsedObjectPool,
	}
}

func wrapHandler(handler func(http.ResponseWriter, *http.Request) (g.Node, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		node, err := handler(w, r)
		if err != nil {
			st := status.Convert(err)
			http.Error(w, err.Error(), bb_http.StatusCodeFromGRPCCode(st.Code()))
			return
		}

		if err := node.Render(w); err != nil {
			http.Error(w, "error rendering node: "+err.Error(), http.StatusInternalServerError)
		}
	}
}

// RegisterHandlers registers handlers for serving web pages in a mux.
func (s *BrowserService) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/{$}", wrapHandler(s.doWelcome))
	mux.HandleFunc(
		"/evaluation/{instance_name}/{reference_format}/{reference}/{key}",
		wrapHandler(s.doEvaluation),
	)
	mux.HandleFunc(
		"/object/{instance_name}/{reference_format}/{reference}/message/{message_type}",
		wrapHandler(s.doMessageObject),
	)
	mux.HandleFunc(
		"/object/{instance_name}/{reference_format}/{reference}/message_list/{message_type}",
		wrapHandler(s.doMessageListObject),
	)
	mux.HandleFunc(
		"/object/{instance_name}/{reference_format}/{reference}/raw",
		wrapHandler(s.doRawObject),
	)
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

func (s *BrowserService) doWelcome(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	return renderPage("Bonanza Browser", []g.Node{
		h.Div(
			h.Class("mx-auto p-4 max-w-[100rem]"),

			h.Div(
				h.Class("card bg-base-200 p-4 shadow"),
				h.H1(
					h.Class("card-title text-2xl mb-4"),
					g.Text("Welcome to Bonanza Browser!"),
				),

				h.P(g.Text("TODO: Document this service.")),
			),
		),
	}), nil
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
		object.GlobalReference{
			InstanceName:   object.NewInstanceName(r.PathValue("instance_name")),
			LocalReference: localReference.Value,
		},
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

func setCookie(w http.ResponseWriter, recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) {
	if cookie, err := proto.Marshal(
		&browser_pb.Cookie{
			RecentlyObservedEncoders: recentlyObservedEncoders,
		},
	); err == nil {
		http.SetCookie(w, &http.Cookie{
			Name:     "bonanza_browser",
			Value:    base64.RawURLEncoding.EncodeToString(cookie),
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
	var unmarshaled MessageList[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder]
	if err := json.Unmarshal([]byte(provided), &unmarshaled); err != nil {
		return nil, provided, fmt.Errorf("failed to unmarshal encoder configuration: %w", err)
	}
	return []*browser_pb.RecentlyObservedEncoder{{
		Configuration: unmarshaled,
	}}, "", nil
}

func getEncodersFromRequest(r *http.Request) ([]*browser_pb.RecentlyObservedEncoder, string, error) {
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
	if currentEncoderConfigurationStr == "" {
		if marshaled, err := json.MarshalIndent(
			MessageList[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder](
				recentlyObservedEncoders[0].Configuration,
			),
			/* prefix = */ "",
			/* indent = */ "  ",
		); err == nil {
			currentEncoderConfigurationStr = string(marshaled)
		}
	}
	return recentlyObservedEncoders, currentEncoderConfigurationStr, err
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
	recentlyObservedEncodersNodes := []g.Node{
		h.Class("card bg-base-200 w-full p-4 shadow"),
		h.H1(
			h.Class("card-title text-2xl"),
			g.Text("Recently observed encoders"),
		),
	}
	for _, recentlyObservedEncoder := range recentlyObservedEncoders {
		l := MessageList[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder](recentlyObservedEncoder.Configuration)
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
	recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder,
) g.Node {
	setCookie(w, recentlyObservedEncoders)

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
				renderEncoderSelector(recentlyObservedEncoders, currentEncoderConfiguration)...,
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
		now: time.Now(),
	}
	keyJSONNodes := jsonRenderer.renderTopLevelMessage(
		model_core.NewTopLevelMessage(keyAny.Message.ProtoReflect(), keyAny.OutgoingReferences),
	)

	recentlyObservedEncoders, currentEncoderConfigurationStr, err := getEncodersFromRequest(r)
	if err != nil {
		errNodes := renderErrorAlert(fmt.Errorf("failed to obtain encoder configuration: %w", err))
		return renderEvaluationPage(
			w,
			evaluationListReference,
			keyJSONNodes,
			errNodes,
			errNodes,
			currentEncoderConfigurationStr,
			recentlyObservedEncoders,
		), nil
	}

	binaryEncoder, err := model_encoding.NewBinaryEncoderFromProto(
		recentlyObservedEncoders[0].Configuration,
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
			recentlyObservedEncoders,
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
			model_parser.NewMessageListObjectParser[object.LocalReference, model_evaluation_pb.Evaluation](),
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
			recentlyObservedEncoders,
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
				return bytes.Compare(keyBytes, marshaledKey), nil
			case *model_evaluation_pb.Evaluation_Parent_:
				return bytes.Compare(keyBytes, level.Parent.FirstKey), level.Parent.Reference
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
			recentlyObservedEncoders,
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
				recentlyObservedEncoders,
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
					model_parser.NewMessageListObjectParser[object.LocalReference, model_evaluation_pb.Dependency](),
				),
			)
			var errIter error
			var nodes []g.Node
			for dependency := range btree.AllLeaves(
				ctx,
				dependencyListReader,
				model_core.Nested(evaluation, dependencies),
				func(element model_core.Message[*model_evaluation_pb.Dependency, object.LocalReference]) (*model_core_pb.DecodableReference, error) {
					if level, ok := element.Message.Level.(*model_evaluation_pb.Dependency_Parent_); ok {
						return level.Parent.Reference, nil
					}
					return nil, nil
				},
				&errIter,
			) {
				if dependencyLeaf, ok := dependency.Message.Level.(*model_evaluation_pb.Dependency_LeafKey); ok {
					cardNodes := []g.Node{
						h.Class("block card my-2 p-4 bg-neutral text-neutral-content font-mono h-auto! overflow-x-auto"),
					}
					if dependencyKey, err := model_core.FlattenAny(model_core.Nested(dependency, dependencyLeaf.LeafKey)); err == nil {
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
						jsonRenderer.renderMessage(model_core.Nested(dependency, dependencyLeaf.LeafKey.ProtoReflect()))...,
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
	newRecentlyObservedEncoders := trimRecentlyObservedEncoders(
		append(
			append(
				[]*browser_pb.RecentlyObservedEncoder{recentlyObservedEncoders[0]},
				jsonRenderer.observedEncoders...,
			),
			recentlyObservedEncoders[1:]...,
		),
	)
	return renderEvaluationPage(
		w,
		evaluationListReference,
		keyJSONNodes,
		valueNodes,
		dependenciesNodes,
		currentEncoderConfigurationStr,
		newRecentlyObservedEncoders,
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
	recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder,
	payload []g.Node,
) g.Node {
	setCookie(w, recentlyObservedEncoders)

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
				renderEncoderSelector(recentlyObservedEncoders, currentEncoderConfiguration)...,
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

	recentlyObservedEncoders, currentEncoderConfigurationStr, err := getEncodersFromRequest(r)
	if err != nil {
		return renderObjectPage(
			w,
			objectReference,
			payloadRenderers,
			currentPayloadRendererIndex,
			currentEncoderConfigurationStr,
			recentlyObservedEncoders,
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
			recentlyObservedEncoders,
			renderErrorAlert(fmt.Errorf("failed to download object: %w", err)),
		), nil
	}
	rendered, encodersInObject := payloadRenderers[currentPayloadRendererIndex].render(
		r,
		model_core.CopyDecodable(objectReference, o),
		recentlyObservedEncoders[0].Configuration,
	)

	// Rendering might reveal the existence of additional encoders.
	newRecentlyObservedEncoders := trimRecentlyObservedEncoders(
		append(
			append(
				[]*browser_pb.RecentlyObservedEncoder{recentlyObservedEncoders[0]},
				encodersInObject...,
			),
			recentlyObservedEncoders[1:]...,
		),
	)

	return renderObjectPage(
		w,
		objectReference,
		payloadRenderers,
		currentPayloadRendererIndex,
		currentEncoderConfigurationStr,
		newRecentlyObservedEncoders,
		rendered,
	), nil
}

func (s *BrowserService) doMessageObject(w http.ResponseWriter, r *http.Request) (g.Node, error) {
	return s.doObject(
		w,
		r,
		[]payloadRenderer{
			rawPayloadRenderer{},
			decodedPayloadRenderer{},
			messageJSONPayloadRenderer{},
		},
		2,
	)
}

func (s *BrowserService) doMessageListObject(w http.ResponseWriter, r *http.Request) (g.Node, error) {
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

type MessageList[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
] []*TMessage

var (
	_ json.Marshaler   = MessageList[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder]{}
	_ json.Unmarshaler = &MessageList[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder]{}
)

func (ml MessageList[TMessage, TMessagePtr]) MarshalJSON() ([]byte, error) {
	b := []byte("[")
	for i, m := range ml {
		var err error
		b, err = protojson.MarshalOptions{}.MarshalAppend(b, TMessagePtr(m))
		if err != nil {
			return nil, err
		}
		if i != len(ml)-1 {
			b = append(b, ',')
		}
	}
	return append(b, ']'), nil
}

func (ml *MessageList[TMessage, TMessagePtr]) UnmarshalJSON(b []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(b))
	t, err := decoder.Token()
	if err != nil {
		return err
	}
	if t != json.Delim('[') {
		return errors.New("expected start of list")
	}

	var values MessageList[TMessage, TMessagePtr]
	for decoder.More() {
		var value messageUnmarshaler[TMessage, TMessagePtr]
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		values = append(values, &value.message)
	}
	*ml = values
	return nil
}

type messageUnmarshaler[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
] struct {
	message TMessage
}

var _ json.Unmarshaler = &messageUnmarshaler[model_encoding_pb.BinaryEncoder, *model_encoding_pb.BinaryEncoder]{}

func (ml *messageUnmarshaler[TMessage, TMessagePtr]) UnmarshalJSON(b []byte) error {
	return protojson.Unmarshal(b, TMessagePtr(&ml.message))
}

// payloadRenderer implements a strategy for rendering the contents of
// an object as HTML.
type payloadRenderer interface {
	queryParameter() string
	name() string
	render(r *http.Request, o model_core.Decodable[*object.Contents], encoders []*model_encoding_pb.BinaryEncoder) ([]g.Node, []*browser_pb.RecentlyObservedEncoder)
}

// rawPayloadRenderer renders an object without performing any decoding
// steps. The output resembles that of "hexdump -C".
type rawPayloadRenderer struct{}

var _ payloadRenderer = rawPayloadRenderer{}

func (rawPayloadRenderer) queryParameter() string { return "raw" }
func (rawPayloadRenderer) name() string           { return "Raw" }

func (rawPayloadRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], encoders []*model_encoding_pb.BinaryEncoder) ([]g.Node, []*browser_pb.RecentlyObservedEncoder) {
	return []g.Node{
		h.Pre(g.Text(hex.Dump(o.Value.GetPayload()))),
	}, nil
}

func decodeObject(o model_core.Decodable[*object.Contents], encoders []*model_encoding_pb.BinaryEncoder) ([]byte, error) {
	objectReference := o.Value.GetReference()
	binaryEncoder, err := model_encoding.NewBinaryEncoderFromProto(
		encoders,
		uint32(objectReference.GetReferenceFormat().GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}
	decodedObject, err := binaryEncoder.DecodeBinary(o.Value.GetPayload(), o.GetDecodingParameters())
	if err != nil {
		return nil, fmt.Errorf("failed to decode object: %w", err)
	}
	return decodedObject, nil
}

// decodedPayloadRenderer renders the decoded payload of an object. The
// output resembles that of "hexdump -C".
type decodedPayloadRenderer struct{}

var _ payloadRenderer = decodedPayloadRenderer{}

func (decodedPayloadRenderer) queryParameter() string { return "decoded" }
func (decodedPayloadRenderer) name() string           { return "Decoded" }

func (decodedPayloadRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], encoders []*model_encoding_pb.BinaryEncoder) ([]g.Node, []*browser_pb.RecentlyObservedEncoder) {
	decodedObject, err := decodeObject(o, encoders)
	if err != nil {
		return renderErrorAlert(err), nil
	}
	return []g.Node{
		h.Pre(g.Text(hex.Dump(decodedObject))),
	}, nil
}

// textPayloadRenderer renders the decoded payload of an object,
// assuming it is UTF-8 encoded text. Any non-UTF-8 byte sequences are
// replaced with U+FFFD "�". If the number of invalid characters is too
// high, output is suppressed.
type textPayloadRenderer struct{}

var _ payloadRenderer = textPayloadRenderer{}

func (textPayloadRenderer) queryParameter() string { return "text" }
func (textPayloadRenderer) name() string           { return "Text" }

func (textPayloadRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], encoders []*model_encoding_pb.BinaryEncoder) ([]g.Node, []*browser_pb.RecentlyObservedEncoder) {
	decodedObject, err := decodeObject(o, encoders)
	if err != nil {
		return renderErrorAlert(err), nil
	}

	if utf8.Valid(decodedObject) {
		// Fast path: byte slice is already valid UTF-8.
		return []g.Node{h.Pre(g.Text(string(decodedObject)))}, nil
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
		return renderErrorAlert(errors.New("object contains binary data, or the encoder configuration is incorrect")), nil
	}

	return []g.Node{h.Pre(g.Text(sb.String()))}, nil
}

// messageJSONRenderer is capable of rendering Protobuf messages as JSON
// with syntax highlighting applied. Any references to other objects
// contained in these messages are rendered as clickable links.
type messageJSONRenderer struct {
	basePath string
	now      time.Time

	observedEncoders []*browser_pb.RecentlyObservedEncoder
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
		if r, ok := value.Message.Message().Interface().(*model_core_pb.DecodableReference); ok {
			if reference, err := model_core.FlattenDecodableReference(model_core.Nested(value, r)); err == nil {
				if fieldOptions, ok := fieldDescriptor.Options().(*descriptorpb.FieldOptions); ok {
					// Field is a valid reference for
					// which we have type information in
					// the field options. Emit a link to
					// the object.
					objectFormat := proto.GetExtension(fieldOptions, model_core_pb.E_ObjectFormat).(*model_core_pb.ObjectFormat)
					rawReference := model_core.DecodableLocalReferenceToString(reference)
					var link string
					switch format := objectFormat.GetFormat().(type) {
					case *model_core_pb.ObjectFormat_Raw:
						link = path.Join(d.basePath, rawReference, "raw")
					case *model_core_pb.ObjectFormat_MessageTypeName:
						link = path.Join(d.basePath, rawReference, "message", format.MessageTypeName)
					case *model_core_pb.ObjectFormat_MessageListTypeName:
						link = path.Join(d.basePath, rawReference, "message_list", format.MessageListTypeName)
					default:
						return []g.Node{
							h.Span(
								h.Class("text-red-600"),
								g.Text("[ Reference field with unknown object format ]"),
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
type messageJSONPayloadRenderer struct{}

var _ payloadRenderer = messageJSONPayloadRenderer{}

func (messageJSONPayloadRenderer) queryParameter() string { return "json" }
func (messageJSONPayloadRenderer) name() string           { return "JSON" }

func (messageJSONPayloadRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], encoders []*model_encoding_pb.BinaryEncoder) ([]g.Node, []*browser_pb.RecentlyObservedEncoder) {
	decodedObject, err := decodeObject(o, encoders)
	if err != nil {
		return renderErrorAlert(err), nil
	}

	messageTypeStr := r.PathValue("message_type")
	messageType, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(messageTypeStr))
	if err != nil {
		return renderErrorAlert(fmt.Errorf("invalid message type %#v: %w", messageTypeStr, err)), nil
	}

	message := messageType.New()
	if err := proto.Unmarshal(decodedObject, message.Interface()); err != nil {
		return renderErrorAlert(fmt.Errorf("failed to unmarshal message: %w", err)), nil
	}
	d := messageJSONRenderer{
		basePath: "../..",
		now:      time.Now(),
	}
	rendered := d.renderTopLevelMessage(model_core.NewTopLevelMessage(message, o.Value))
	return rendered, d.observedEncoders
}

// messageListJSONPayloadRenderer renders the decoded payload of an
// object, assuming it is a varint separated list of Protobuf messages
// that can be converted to JSON.
type messageListJSONPayloadRenderer struct{}

var _ payloadRenderer = messageListJSONPayloadRenderer{}

func (messageListJSONPayloadRenderer) queryParameter() string { return "json" }
func (messageListJSONPayloadRenderer) name() string           { return "JSON" }

func (messageListJSONPayloadRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], encoders []*model_encoding_pb.BinaryEncoder) ([]g.Node, []*browser_pb.RecentlyObservedEncoder) {
	decodedObject, err := decodeObject(o, encoders)
	if err != nil {
		return renderErrorAlert(err), nil
	}

	messageTypeStr := r.PathValue("message_type")
	messageType, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(messageTypeStr))
	if err != nil {
		return renderErrorAlert(fmt.Errorf("invalid message type %#v: %w", messageTypeStr, err)), nil
	}

	var elements []protoreflect.Message
	originalDataLength := len(decodedObject)
	for len(decodedObject) > 0 {
		// Extract the size of the element.
		offset := originalDataLength - len(decodedObject)
		length, lengthLength := varint.ConsumeForward[uint](decodedObject)
		if lengthLength < 0 {
			return renderErrorAlert(fmt.Errorf("invalid element length at offset %d", offset)), nil
		}

		// Validate the size.
		decodedObject = decodedObject[lengthLength:]
		if length > uint(len(decodedObject)) {
			return renderErrorAlert(fmt.Errorf("length of element at offset %d is %d bytes, which exceeds maximum permitted size of %d bytes", offset, length, len(decodedObject))), nil
		}

		// Unmarshal the element.
		element := messageType.New()
		if err := proto.Unmarshal(decodedObject[:length], element.Interface()); err != nil {
			return renderErrorAlert(fmt.Errorf("failed to unmarshal element at offset %d: %w", offset, err)), nil
		}
		elements = append(elements, element)
		decodedObject = decodedObject[length:]
	}

	d := messageJSONRenderer{
		basePath: "../..",
		now:      time.Now(),
	}
	rendered := d.renderMessageList(model_core.NewMessage(elements, o.Value))
	return rendered, d.observedEncoders
}
