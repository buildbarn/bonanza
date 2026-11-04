package starlark

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"bonanza.build/pkg/glob"
	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/types/known/emptypb"
)

// BuildSetting is a Starlark build setting object that may be provided
// to rule(build_setting=...) to indicate that targets of the rule
// behave as user-defined build settings.
type BuildSetting[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	buildSettingType BuildSettingType[TReference, TMetadata]
	flag             bool
}

var _ eval.Value = (*BuildSetting[object.LocalReference, model_core.CreatedObjectTree])(nil)

// NewBuildSetting creates a new Starlark build setting object.
//
// When the flag argument is set, it will be possible to override the
// build setting value of targets of the rule on the command line.
func NewBuildSetting[TReference any, TMetadata model_core.ReferenceMetadata](buildSettingType BuildSettingType[TReference, TMetadata], flag bool) *BuildSetting[TReference, TMetadata] {
	return &BuildSetting[TReference, TMetadata]{
		buildSettingType: buildSettingType,
		flag:             flag,
	}
}

// Repr writes the representation of a Starlark build setting object.
func (bs *BuildSetting[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString(fmt.Sprintf("<config.%s>", bs.buildSettingType.Type()))
	return nil
}

// Type returns the type name of a Starlark build setting object in
// string form.
func (bs *BuildSetting[TReference, TMetadata]) Type() string {
	return "config." + bs.buildSettingType.Type()
}

// Truth returns whether a Starlark build setting object is a "truthy"
// or a "falsy". Starlark build setting objects are always "truthy".
func (BuildSetting[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

// Encode a Starlark build setting object to a Protobuf message, so that
// it can be written to storage.
func (bs *BuildSetting[TReference, TMetadata]) Encode() *model_starlark_pb.BuildSetting {
	buildSetting := model_starlark_pb.BuildSetting{
		Flag: bs.flag,
	}
	bs.buildSettingType.Encode(&buildSetting)
	return &buildSetting
}

// BuildSettingType represents the type of a build setting that is
// created by calling one of the functions in config.*(), such as
// config.bool() or config.string().
type BuildSettingType[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	Type() string
	Encode(out *model_starlark_pb.BuildSetting)
	GetCanonicalizer(currentPackage pg_label.CanonicalPackage) BuildSettingCanonicalizer

	// GetAttrType returns the type of the attribute that
	// corresponds to this build setting type. This is used to
	// declare the "build_setting_default" attribute when symbolic
	// macros inherit the attributes of a build setting rule.
	GetAttrType() AttrType[TReference, TMetadata]
}

// BuildSettingCanonicalizer can be used to convert Starlark values to a
// canonical representation for assignment to a build setting.
//
// This interface also provides a method for converting lists of strings
// to a canonical Starlark value. This is used to process build setting
// overrides that are provided on the command line (e.g.,
// --@rules_go//go/config:gc_goopts=-e).
type BuildSettingCanonicalizer interface {
	unpack.Canonicalizer

	CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (eval.Value, error)
}

type boolBuildSettingType[TReference any, TMetadata model_core.ReferenceMetadata] struct{}

// NewBoolBuildSettingType creates a Boolean build setting type. These are
// normally constructed by calling config.bool().
func NewBoolBuildSettingType[TReference any, TMetadata model_core.ReferenceMetadata]() BuildSettingType[TReference, TMetadata] {
	return boolBuildSettingType[TReference, TMetadata]{}
}

func (boolBuildSettingType[TReference, TMetadata]) Type() string {
	return "bool"
}

func (boolBuildSettingType[TReference, TMetadata]) Encode(out *model_starlark_pb.BuildSetting) {
	out.Type = &model_starlark_pb.BuildSetting_Bool{
		Bool: &emptypb.Empty{},
	}
}

func (boolBuildSettingType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) BuildSettingCanonicalizer {
	return &boolBuildSettingCanonicalizer{
		Canonicalizer: unpack.Bool,
	}
}

func (boolBuildSettingType[TReference, TMetadata]) GetAttrType() AttrType[TReference, TMetadata] {
	return NewBoolAttrType[TReference, TMetadata]()
}

type boolBuildSettingCanonicalizer struct {
	unpack.Canonicalizer
}

// ParseBoolBuildSettingString converts a string value that can be
// assigned to a Boolean-type build setting to its actual Boolean value.
// This conversion needs to be performed when overrides for build
// settings are specified on the command line, or if matching of flag
// values is performed in config_setting().
func ParseBoolBuildSettingString(s string) (bool, error) {
	switch s {
	case "0", "false", "False":
		return false, nil
	case "1", "true", "True":
		return true, nil
	default:
		return false, fmt.Errorf("booleans can only have values \"0\", \"1\", \"false\", \"true\", \"False\" and \"True\", not %#v", s)
	}
}

func (boolBuildSettingCanonicalizer) CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (eval.Value, error) {
	v, err := ParseBoolBuildSettingString(values[len(values)-1])
	if err != nil {
		return nil, err
	}
	return eval.Bool(v), nil
}

type intBuildSettingType[TReference any, TMetadata model_core.ReferenceMetadata] struct{}

// NewIntBuildSettingType creates an integer build setting type. These are
// normally constructed by calling config.int().
func NewIntBuildSettingType[TReference any, TMetadata model_core.ReferenceMetadata]() BuildSettingType[TReference, TMetadata] {
	return intBuildSettingType[TReference, TMetadata]{}
}

func (intBuildSettingType[TReference, TMetadata]) Type() string {
	return "int"
}

func (intBuildSettingType[TReference, TMetadata]) Encode(out *model_starlark_pb.BuildSetting) {
	out.Type = &model_starlark_pb.BuildSetting_Int{
		Int: &emptypb.Empty{},
	}
}

func (intBuildSettingType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) BuildSettingCanonicalizer {
	return &intBuildSettingCanonicalizer{
		Canonicalizer: unpack.Int[int32](),
	}
}

func (intBuildSettingType[TReference, TMetadata]) GetAttrType() AttrType[TReference, TMetadata] {
	return NewIntAttrType[TReference, TMetadata](nil)
}

type intBuildSettingCanonicalizer struct {
	unpack.Canonicalizer
}

func (intBuildSettingCanonicalizer) CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (eval.Value, error) {
	v, err := strconv.ParseInt(values[len(values)-1], 10, 32)
	if err != nil {
		return nil, err
	}
	return eval.MakeInt64(v), nil
}

type labelBuildSettingCanonicalizer[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	unpack.Canonicalizer
	currentPackage pg_label.CanonicalPackage
	singletonList  bool
}

// NewLabelBuildSettingCanonicalizer creates a BuildSettingCanonicalizer
// that is capable of canonicalizing values assigned to label_setting()s
// and label_flag()s.
//
// As label_setting()s and label_flag()s are implemented as a kind of
// alias() and not as a rule target, this type does not have an
// associated BuildSettingType. This is why this
// BuildSettingCanonicalizer can be constructed independently.
func NewLabelBuildSettingCanonicalizer[TReference any, TMetadata model_core.ReferenceMetadata](currentPackage pg_label.CanonicalPackage, singletonList bool) BuildSettingCanonicalizer {
	labelSettingUnpackerInto := NewLabelOrStringUnpackerInto[TReference, TMetadata](currentPackage)
	var canonicalizer unpack.Canonicalizer
	if singletonList {
		canonicalizer = unpack.Or([]unpack.UnpackerInto[[]pg_label.ResolvedLabel]{
			unpack.Singleton(labelSettingUnpackerInto),
			// This allows us to set this build setting to a
			// list with multiple values, which is not what
			// we want.
			// TODO: Add a custom unpacker to forbid this.
			unpack.List(labelSettingUnpackerInto),
		})
	} else {
		canonicalizer = unpack.IfNotNone(labelSettingUnpackerInto)
	}
	return &labelBuildSettingCanonicalizer[TReference, TMetadata]{
		Canonicalizer:  canonicalizer,
		currentPackage: currentPackage,
		singletonList:  singletonList,
	}
}

func (bsc *labelBuildSettingCanonicalizer[TReference, TMetadata]) CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (eval.Value, error) {
	apparentLabel, err := bsc.currentPackage.AppendLabel(values[len(values)-1])
	if err != nil {
		return nil, err
	}
	resolvedLabel, err := pg_label.Resolve(labelResolver, bsc.currentPackage.GetCanonicalRepo(), apparentLabel)
	if err != nil {
		return nil, err
	}

	var value eval.Value = NewLabel[TReference, TMetadata](resolvedLabel)
	if bsc.singletonList {
		value = eval.NewList([]eval.Value{value})
	}
	return value, nil
}

type labelListBuildSettingType[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	repeatable bool
}

// NewLabelListBuildSettingType creates a label list build setting type.
// This is a Bonanza specific extension that's provided so that flags
// like --extra_execution_platforms can be declared in Starlark.
func NewLabelListBuildSettingType[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](repeatable bool) BuildSettingType[TReference, TMetadata] {
	return labelListBuildSettingType[TReference, TMetadata]{
		repeatable: repeatable,
	}
}

func (labelListBuildSettingType[TReference, TMetadata]) Type() string {
	return "label_list"
}

func (bst labelListBuildSettingType[TReference, TMetadata]) Encode(out *model_starlark_pb.BuildSetting) {
	out.Type = &model_starlark_pb.BuildSetting_LabelList{
		LabelList: &model_starlark_pb.BuildSetting_ListType{
			Repeatable: bst.repeatable,
		},
	}
}

func (bst labelListBuildSettingType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) BuildSettingCanonicalizer {
	return &labelListBuildSettingCanonicalizer[TReference, TMetadata]{
		Canonicalizer:  unpack.List(NewLabelOrStringUnpackerInto[TReference, TMetadata](currentPackage)),
		currentPackage: currentPackage,
		repeatable:     bst.repeatable,
	}
}

func (labelListBuildSettingType[TReference, TMetadata]) GetAttrType() AttrType[TReference, TMetadata] {
	return NewLabelListAttrType(
		glob.NFAMatchingNothing.Bytes(),
		NewProtoTransitionDefinition[TReference, TMetadata](model_core.NewSimpleMessage[TReference](&TargetTransition)),
	)
}

type labelListBuildSettingCanonicalizer[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	unpack.Canonicalizer

	currentPackage pg_label.CanonicalPackage
	repeatable     bool
}

func (labelListBuildSettingCanonicalizer[TReference, TMetadata]) CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (eval.Value, error) {
	return nil, errors.New("TODO: Implement canonicalization of config.label_list")
}

type stringBuildSettingType[TReference any, TMetadata model_core.ReferenceMetadata] struct{}

// NewStringBuildSettingType creates a string build setting type. These are
// normally constructed by calling config.string().
func NewStringBuildSettingType[TReference any, TMetadata model_core.ReferenceMetadata]() BuildSettingType[TReference, TMetadata] {
	return stringBuildSettingType[TReference, TMetadata]{}
}

func (stringBuildSettingType[TReference, TMetadata]) Type() string {
	return "string"
}

func (stringBuildSettingType[TReference, TMetadata]) Encode(out *model_starlark_pb.BuildSetting) {
	out.Type = &model_starlark_pb.BuildSetting_String_{
		String_: &emptypb.Empty{},
	}
}

func (stringBuildSettingType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) BuildSettingCanonicalizer {
	return &stringBuildSettingCanonicalizer{
		Canonicalizer: unpack.String,
	}
}

func (stringBuildSettingType[TReference, TMetadata]) GetAttrType() AttrType[TReference, TMetadata] {
	return NewStringAttrType[TReference, TMetadata](nil)
}

type stringBuildSettingCanonicalizer struct {
	unpack.Canonicalizer
}

func (stringBuildSettingCanonicalizer) CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (eval.Value, error) {
	return eval.String(values[len(values)-1]), nil
}

type stringListBuildSettingType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	repeatable bool
}

// NewStringListBuildSettingType creates a string list build setting
// type. These are normally constructed by calling config.string_list().
func NewStringListBuildSettingType[TReference any, TMetadata model_core.ReferenceMetadata](repeatable bool) BuildSettingType[TReference, TMetadata] {
	return stringListBuildSettingType[TReference, TMetadata]{
		repeatable: repeatable,
	}
}

func (stringListBuildSettingType[TReference, TMetadata]) Type() string {
	return "string_list"
}

func (bst stringListBuildSettingType[TReference, TMetadata]) Encode(out *model_starlark_pb.BuildSetting) {
	out.Type = &model_starlark_pb.BuildSetting_StringList{
		StringList: &model_starlark_pb.BuildSetting_ListType{
			Repeatable: bst.repeatable,
		},
	}
}

func (bst stringListBuildSettingType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) BuildSettingCanonicalizer {
	return &stringListBuildSettingCanonicalizer{
		Canonicalizer: unpack.List(unpack.String),
		repeatable:    bst.repeatable,
	}
}

func (stringListBuildSettingType[TReference, TMetadata]) GetAttrType() AttrType[TReference, TMetadata] {
	return NewStringListAttrType[TReference, TMetadata]()
}

type stringListBuildSettingCanonicalizer struct {
	unpack.Canonicalizer
	repeatable bool
}

func (bsc stringListBuildSettingCanonicalizer) CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (eval.Value, error) {
	if !bsc.repeatable {
		if lastValue := values[len(values)-1]; lastValue == "" {
			values = nil
		} else {
			values = strings.Split(lastValue, ",")
		}
	}

	elements := make([]eval.Value, 0, len(values))
	for _, value := range values {
		elements = append(elements, eval.String(value))
	}
	return eval.NewList(elements), nil
}
