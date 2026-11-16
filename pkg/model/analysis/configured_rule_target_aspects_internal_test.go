package analysis

import (
	"testing"

	"bonanza.build/pkg/label"
	"bonanza.build/pkg/model/chunking"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/types/known/emptypb"
)

func TestRuleTargetAnalysisGetAspectParameters(t *testing.T) {
	referenceFormat := object.SHA256V1ReferenceFormat
	encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
	a := &ruleTargetAnalysis[object.LocalReference, model_core.CreatedObjectTree]{
		computer: &baseComputer[object.LocalReference, model_core.CreatedObjectTree]{
			referenceFormat: referenceFormat,
		},
		thread:           eval.NewThread(t.Context(), nil),
		analysisEncoders: &AnalysisEncoders[object.LocalReference]{Encoder: encoder},
		ruleIdentifier:   util.Must(label.NewCanonicalStarlarkIdentifier("@@rules+//:defs.bzl%my_rule")),
		attrValues: map[string]any{
			"flag": eval.Bool(true),
			"mode": eval.String("fast"),
			"off":  eval.Bool(false),
		},
	}
	valueEncodingOptions := &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{
		Context:               t.Context(),
		ObjectEncoder:         encoder,
		ObjectReferenceFormat: referenceFormat,
		ObjectChunkingParameters: util.Must(chunking.NewParametersFromProto(&model_chunking_pb.Parameters{
			MinimumSizeBytes: 32 * 1024,
			HorizonSizeBytes: 8 * 32 * 1024,
		}, referenceFormat)),
	}
	boolAttr := func(defaultValue bool) *model_starlark_pb.Attr {
		return &model_starlark_pb.Attr{
			Default: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Bool{Bool: defaultValue}},
			Type:    &model_starlark_pb.Attr_Bool{Bool: &emptypb.Empty{}},
		}
	}
	stringAttr := func(values ...string) *model_starlark_pb.Attr {
		return &model_starlark_pb.Attr{
			Default: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Str{Str: &model_filesystem_pb.MaybeInlineFileContents{}}},
			Type:    &model_starlark_pb.Attr_String_{String_: &model_starlark_pb.Attr_StringType{Values: values}},
		}
	}
	getAspectParameters := func(publicAttrs ...*model_starlark_pb.NamedAttr) (*model_core.Decodable[model_core.CreatedObject[model_core.CreatedObjectTree]], error) {
		return a.getAspectParameters(
			model_core.NewSimpleMessage[object.LocalReference](&model_analysis_pb.LabelAttrAspects_Value_Aspect{
				Identifier:  "@@rules+//:defs.bzl%my_aspect",
				PublicAttrs: publicAttrs,
			}),
			valueEncodingOptions,
		)
	}

	t.Run("NoPublicAttrs", func(t *testing.T) {
		parameters, err := getAspectParameters()
		require.NoError(t, err)
		require.Nil(t, parameters)
	})

	t.Run("DefaultValues", func(t *testing.T) {
		// Values equal to the default value are omitted. If no
		// values remain, no object is created.
		parameters, err := getAspectParameters(
			&model_starlark_pb.NamedAttr{Name: "flag", Attr: boolAttr(true)},
			&model_starlark_pb.NamedAttr{Name: "off", Attr: boolAttr(false)},
		)
		require.NoError(t, err)
		require.Nil(t, parameters)
	})

	t.Run("NonDefaultValues", func(t *testing.T) {
		parameters, err := getAspectParameters(
			&model_starlark_pb.NamedAttr{Name: "flag", Attr: boolAttr(false)},
			&model_starlark_pb.NamedAttr{Name: "mode", Attr: stringAttr("fast", "slow")},
		)
		require.NoError(t, err)
		require.NotNil(t, parameters)

		// Equal parameters should yield the same object.
		parameters2, err := getAspectParameters(
			&model_starlark_pb.NamedAttr{Name: "flag", Attr: boolAttr(false)},
			&model_starlark_pb.NamedAttr{Name: "mode", Attr: stringAttr("fast", "slow")},
		)
		require.NoError(t, err)
		require.Equal(t, parameters.Value.GetLocalReference(), parameters2.Value.GetLocalReference())
	})

	t.Run("MissingRuleAttr", func(t *testing.T) {
		_, err := getAspectParameters(
			&model_starlark_pb.NamedAttr{Name: "missing", Attr: boolAttr(false)},
		)
		require.EqualError(t, err, "aspect \"@@rules+//:defs.bzl%my_aspect\" requires rule \"@@rules+//:defs.bzl%my_rule\" to have attr \"missing\"")
	})

	t.Run("DisallowedValue", func(t *testing.T) {
		_, err := getAspectParameters(
			&model_starlark_pb.NamedAttr{Name: "mode", Attr: stringAttr("slow")},
		)
		require.EqualError(t, err, "attr \"mode\" of aspect \"@@rules+//:defs.bzl%my_aspect\" does not permit value \"fast\"")
	})

	t.Run("WrongType", func(t *testing.T) {
		_, err := getAspectParameters(
			&model_starlark_pb.NamedAttr{Name: "mode", Attr: boolAttr(false)},
		)
		require.ErrorContains(t, err, "attr \"mode\" of aspect \"@@rules+//:defs.bzl%my_aspect\":")
	})
}
