package query

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"bonanza.build/pkg/bazelclient/arguments"
	"bonanza.build/pkg/bazelclient/commands"
	"bonanza.build/pkg/bazelclient/commands/build"
	"bonanza.build/pkg/bazelclient/formatted"
	"bonanza.build/pkg/bazelclient/logging"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_parser "bonanza.build/pkg/model/parser"
	"bonanza.build/pkg/model/render"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"

	"golang.org/x/term"
	"google.golang.org/protobuf/proto"
)

// DoQuery implements the "bazel query" command, which evaluates a query
// expression against the targets in the current workspace, and prints
// the targets yielded by it.
func DoQuery(args *arguments.QueryCommand, workspacePath path.Parser) {
	logger := logging.NewLoggerFromFlags(&args.CommonFlags)
	commands.ValidateInsideWorkspace(logger, "query", workspacePath)

	// Like Bazel, accept query expressions that are spread across
	// multiple arguments.
	queryString := strings.Join(args.Arguments, " ")
	if queryString == "" {
		logger.Fatal(formatted.Text("Missing query expression"))
	}

	// Only request as much of the definitions of targets as needed
	// to print the results. When only printing packages, let the
	// query yield packages instead of targets, so that this does
	// not require traversing all targets.
	output := args.QueryFlags.Output
	var definitionDetail model_analysis_pb.QueryTarget_DefinitionDetail
	switch output {
	case arguments.QueryOutput_Label, arguments.QueryOutput_Package:
		definitionDetail = model_analysis_pb.QueryTarget_NONE
	case arguments.QueryOutput_LabelKind:
		definitionDetail = model_analysis_pb.QueryTarget_KIND
	case arguments.QueryOutput_Build:
		definitionDetail = model_analysis_pb.QueryTarget_FULL
	default:
		panic("unknown query output format")
	}

	evaluation := build.Evaluate(logger, &args.CommonFlags, workspacePath, "query", func(currentPackage label.CanonicalPackage) proto.Message {
		// Resolve target patterns relative to the current
		// package.
		query, err := label.NewQuery(queryString, currentPackage.AppendTargetPattern)
		if err != nil {
			logger.Fatal(formatted.Textf("Invalid query expression: %s", err))
		}
		if output == arguments.QueryOutput_Package {
			query, err = label.NewQuery("packages("+label.QueryToString(query)+")", label.NewApparentTargetPattern)
			if err != nil {
				logger.Fatal(formatted.Textf("Failed to construct query expression for printing packages: %s", err))
			}
		}
		return &model_analysis_pb.QueryResult_Key{
			Query:               label.QueryToString(query),
			IncludeImplicitDeps: args.QueryFlags.ImplicitDeps,
			DefinitionDetail:    definitionDetail,
		}
	})

	// Apply syntax highlighting to targets printed using
	// --output=build, using the same rules as for log messages.
	writeFormatted := formatted.WritePlainText
	switch args.CommonFlags.Color {
	case arguments.Color_Yes:
		writeFormatted = formatted.WriteVT100
	case arguments.Color_Auto:
		if term.IsTerminal(int(os.Stdout.Fd())) {
			writeFormatted = formatted.WriteVT100
		}
	}

	// Wrap targets printed using --output=build to the width of
	// the terminal. If output is not written to a terminal, use a
	// fixed width, so that the output does not depend on the
	// environment.
	width := defaultBuildOutputWidth
	if terminalWidth, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && terminalWidth > 0 {
		width = terminalWidth
	}

	w := bufio.NewWriter(os.Stdout)
	count, err := printQueryResult(context.Background(), evaluation, output, w, writeFormatted, width)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to print query result: %s", err))
	}
	if err := w.Flush(); err != nil {
		logger.Fatal(formatted.Textf("Failed to write query result: %s", err))
	}
	if count == 0 {
		logger.Info(formatted.Text("Empty results"))
	}
}

// defaultBuildOutputWidth is the maximum width of lines written when
// displaying targets using --output=build, if output is not written to
// a terminal. Values of attributes that don't fit are spread across
// multiple lines.
const defaultBuildOutputWidth = 80

// printQueryResult writes the targets contained in the value of a
// QueryResult to a writer, using the requested output format. Targets
// are written in the order in which they are stored, which is the
// order defined by label.CanonicalLabel.Compare(). It returns the
// number of targets written.
func printQueryResult(ctx context.Context, evaluation *build.Evaluation, output arguments.QueryOutput, w *bufio.Writer, writeFormatted func(formatted.Node, io.StringWriter) (int, error), width int) (int, error) {
	valueAny, err := evaluation.GetRequestedValue(ctx)
	if err != nil {
		return 0, err
	}
	value, err := model_core.UnmarshalAnyNew(valueAny)
	if err != nil {
		return 0, fmt.Errorf("failed to unmarshal value: %w", err)
	}
	queryResult, ok := value.Message.(*model_analysis_pb.QueryResult_Value)
	if !ok {
		return 0, errors.New("value is not of type QueryResult.Value")
	}

	// Objects containing parents of the B-tree of targets and
	// Starlark values are created by the analysis functions, which
	// currently don't apply any encoding to them.
	parsedObjectPoolIngester := evaluation.GetParsedObjectPoolIngester()
	valueObjectEncoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
	targetReader := model_parser.LookupParsedObjectReader(
		parsedObjectPoolIngester,
		model_parser.NewChainedObjectParser(
			model_parser.NewEncodedObjectParser[object.LocalReference](valueObjectEncoder),
			model_parser.NewProtoListObjectParser[object.LocalReference, model_analysis_pb.QueryTarget](),
		),
	)
	starlarkRenderer := render.StarlarkRenderer{
		Context: ctx,
		DictReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](valueObjectEncoder),
				model_parser.NewProtoListObjectParser[object.LocalReference, model_starlark_pb.Dict_Entry](),
			),
		),
		ListReader: model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](valueObjectEncoder),
				model_parser.NewProtoListObjectParser[object.LocalReference, model_starlark_pb.List_Element](),
			),
		),
	}

	count := 0
	var errIter error
	for entry := range btree.AllLeaves(
		ctx,
		targetReader,
		model_core.Nested(value.Decay(), queryResult.Targets),
		func(entry model_core.Message[*model_analysis_pb.QueryTarget, object.LocalReference]) (*model_core_pb.DecodableReference, error) {
			return entry.Message.GetParent().GetReference(), nil
		},
		&errIter,
	) {
		leaf, ok := entry.Message.Level.(*model_analysis_pb.QueryTarget_Leaf_)
		if !ok {
			return count, errors.New("not a valid leaf entry")
		}
		targetLabel, err := label.NewCanonicalLabel(leaf.Leaf.Label)
		if err != nil {
			return count, fmt.Errorf("invalid label %#v: %w", leaf.Leaf.Label, err)
		}
		switch output {
		case arguments.QueryOutput_Label:
			_, err = fmt.Fprintln(w, targetLabel.String())
		case arguments.QueryOutput_LabelKind:
			kind, errKind := model_starlark.GetTargetKind(leaf.Leaf.Definition)
			if errKind != nil {
				return count, fmt.Errorf("failed to obtain kind of target %#v: %w", leaf.Leaf.Label, errKind)
			}
			_, err = fmt.Fprintln(w, kind, targetLabel.String())
		case arguments.QueryOutput_Build:
			// Separate targets by an empty line, as is
			// done in BUILD files.
			doc := starlarkRenderer.Target(
				targetLabel,
				model_core.Nested(entry, leaf.Leaf.Definition),
				leaf.Leaf.PublicAttrNames,
			)
			if _, err = writeFormatted(build.FormatDoc(doc, width, "    "), w); err == nil {
				_, err = w.WriteString("\n")
			}
		case arguments.QueryOutput_Package:
			// The query was wrapped in packages(), meaning
			// that each label corresponds to a package.
			_, err = fmt.Fprintln(w, targetLabel.GetCanonicalPackage().String())
		default:
			panic("unknown query output format")
		}
		if err != nil {
			return count, err
		}
		count++
	}
	return count, errIter
}
