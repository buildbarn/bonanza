package build

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"

	"bonanza.build/pkg/bazelclient/formatted"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_parser "bonanza.build/pkg/model/parser"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/types/known/anypb"
)

// Evaluation contains the results of the successful evaluation of a
// key, as performed by Evaluate().
type Evaluation struct {
	namespace                object.Namespace
	browserURL               string
	referenceFormat          object.ReferenceFormat
	parsedObjectPoolIngester *model_parser.ParsedObjectPoolIngester[object.LocalReference]
	actionEncoder            model_encoding.DeterministicBinaryEncoder
	requestedKey             model_core.TopLevelMessage[*anypb.Any, object.LocalReference]
	marshaledRequestedKey    []byte
	outcomesReference        *model_core.Decodable[object.LocalReference]
}

// GetOutcomesLink returns a reference to the outcomes of the
// evaluation. If the URL of the Bonanza Browser is known, the
// reference links to the page displaying the value of the requested
// key.
func (e *Evaluation) GetOutcomesLink() (formatted.Node, bool) {
	if e.outcomesReference == nil {
		return nil, false
	}
	outcomesReferenceStr := model_core.DecodableLocalReferenceToString(*e.outcomesReference)
	node := formatted.Text(outcomesReferenceStr)
	if e.browserURL != "" {
		if evaluationURL, err := url.JoinPath(
			e.browserURL,
			"evaluation",
			e.namespace.InstanceName.AsURLSafeComponent(),
			e.namespace.ReferenceFormat.ToProto().String(),
			outcomesReferenceStr,
			base64.RawURLEncoding.EncodeToString(e.marshaledRequestedKey),
		); err == nil {
			node = formatted.Link(evaluationURL, node)
		}
	}
	return node, true
}

// GetParsedObjectPoolIngester returns the ingester of the parsed
// object pool that was used to perform the evaluation. It can be used
// to construct readers for objects referenced by the value of the
// requested key.
func (e *Evaluation) GetParsedObjectPoolIngester() *model_parser.ParsedObjectPoolIngester[object.LocalReference] {
	return e.parsedObjectPoolIngester
}

// GetRequestedValue returns the value of the requested key.
func (e *Evaluation) GetRequestedValue(ctx context.Context) (model_core.Message[*model_core_pb.Any, object.LocalReference], error) {
	if e.outcomesReference == nil {
		return model_core.Message[*model_core_pb.Any, object.LocalReference]{}, errors.New("evaluation did not yield any outcomes")
	}
	keyReference, err := model_core.ComputeTopLevelMessageReference(e.requestedKey, e.referenceFormat)
	if err != nil {
		return model_core.Message[*model_core_pb.Any, object.LocalReference]{}, fmt.Errorf("failed to compute reference of requested key: %w", err)
	}

	evaluationsReader := model_parser.LookupParsedObjectReader(
		e.parsedObjectPoolIngester,
		model_parser.NewChainedObjectParser(
			model_parser.NewEncodedObjectParser[object.LocalReference](e.actionEncoder),
			model_parser.NewProtoListObjectParser[object.LocalReference, model_evaluation_pb.Evaluations](),
		),
	)
	outcomes, err := evaluationsReader.ReadObject(ctx, *e.outcomesReference)
	if err != nil {
		return model_core.Message[*model_core_pb.Any, object.LocalReference]{}, fmt.Errorf("failed to read outcomes: %w", err)
	}
	evaluations, err := btree.Find(
		ctx,
		evaluationsReader,
		outcomes,
		func(entry model_core.Message[*model_evaluation_pb.Evaluations, object.LocalReference]) (int, *model_core_pb.DecodableReference) {
			switch level := entry.Message.Level.(type) {
			case *model_evaluation_pb.Evaluations_Leaf_:
				return bytes.Compare(keyReference.GetRawReference(), level.Leaf.KeyReference), nil
			case *model_evaluation_pb.Evaluations_Parent_:
				return bytes.Compare(keyReference.GetRawReference(), level.Parent.FirstKeyReference), level.Parent.Reference
			default:
				return 0, nil
			}
		},
	)
	if err != nil {
		return model_core.Message[*model_core_pb.Any, object.LocalReference]{}, fmt.Errorf("failed to look up requested key in outcomes: %w", err)
	}
	if !evaluations.IsSet() {
		return model_core.Message[*model_core_pb.Any, object.LocalReference]{}, errors.New("requested key is not present in outcomes")
	}
	leaf, ok := evaluations.Message.Level.(*model_evaluation_pb.Evaluations_Leaf_)
	if !ok {
		return model_core.Message[*model_core_pb.Any, object.LocalReference]{}, errors.New("outcomes entry of requested key is not a valid leaf")
	}

	evaluation, err := model_evaluation.GraphletGetEvaluation(
		ctx,
		model_parser.LookupParsedObjectReader(
			e.parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](e.actionEncoder),
				model_parser.NewProtoObjectParser[object.LocalReference, model_evaluation_pb.Evaluation](),
			),
		),
		model_core.Nested(evaluations, leaf.Leaf.Graphlet),
	)
	if err != nil {
		return model_core.Message[*model_core_pb.Any, object.LocalReference]{}, fmt.Errorf("failed to obtain evaluation of requested key: %w", err)
	}
	if evaluation.Message.Value == nil {
		return model_core.Message[*model_core_pb.Any, object.LocalReference]{}, errors.New("evaluation of requested key has no value")
	}
	return model_core.Nested(evaluation, evaluation.Message.Value), nil
}
