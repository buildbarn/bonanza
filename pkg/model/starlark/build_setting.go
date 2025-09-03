package starlark

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/unpack"

	"google.golang.org/protobuf/types/known/emptypb"

	"go.starlark.net/starlark"
)

type BuildSetting struct {
	buildSettingType BuildSettingType
	flag             bool
}

var _ starlark.Value = (*BuildSetting)(nil)

func NewBuildSetting(buildSettingType BuildSettingType, flag bool) *BuildSetting {
	return &BuildSetting{
		buildSettingType: buildSettingType,
		flag:             flag,
	}
}

func (bs *BuildSetting) String() string {
	return fmt.Sprintf("<config.%s>", bs.buildSettingType.Type())
}

func (bs *BuildSetting) Type() string {
	return "config." + bs.buildSettingType.Type()
}

func (bs *BuildSetting) Freeze() {}

func (bs *BuildSetting) Truth() starlark.Bool {
	return starlark.True
}

func (bs *BuildSetting) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, fmt.Errorf("config.%s cannot be hashed", bs.buildSettingType.Type())
}

func (bs *BuildSetting) Encode() *model_starlark_pb.BuildSetting {
	buildSetting := model_starlark_pb.BuildSetting{
		Flag: bs.flag,
	}
	bs.buildSettingType.Encode(&buildSetting)
	return &buildSetting
}

type BuildSettingType interface {
	Type() string
	Encode(out *model_starlark_pb.BuildSetting)
	GetCanonicalizer(currentPackage pg_label.CanonicalPackage) BuildSettingCanonicalizer
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

	CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (starlark.Value, error)
}

type boolBuildSettingType struct{}

var BoolBuildSettingType BuildSettingType = boolBuildSettingType{}

func (boolBuildSettingType) Type() string {
	return "bool"
}

func (boolBuildSettingType) Encode(out *model_starlark_pb.BuildSetting) {
	out.Type = &model_starlark_pb.BuildSetting_Bool{
		Bool: &emptypb.Empty{},
	}
}

func (boolBuildSettingType) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) BuildSettingCanonicalizer {
	return &boolBuildSettingCanonicalizer{
		Canonicalizer: unpack.Bool,
	}
}

type boolBuildSettingCanonicalizer struct {
	unpack.Canonicalizer
}

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

func (boolBuildSettingCanonicalizer) CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (starlark.Value, error) {
	v, err := ParseBoolBuildSettingString(values[len(values)-1])
	if err != nil {
		return nil, err
	}
	return starlark.Bool(v), nil
}

type intBuildSettingType struct{}

var IntBuildSettingType BuildSettingType = intBuildSettingType{}

func (intBuildSettingType) Type() string {
	return "int"
}

func (intBuildSettingType) Encode(out *model_starlark_pb.BuildSetting) {
	out.Type = &model_starlark_pb.BuildSetting_Int{
		Int: &emptypb.Empty{},
	}
}

func (intBuildSettingType) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) BuildSettingCanonicalizer {
	return &intBuildSettingCanonicalizer{
		Canonicalizer: unpack.Int[int32](),
	}
}

type intBuildSettingCanonicalizer struct {
	unpack.Canonicalizer
}

func (intBuildSettingCanonicalizer) CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (starlark.Value, error) {
	v, err := strconv.ParseInt(values[len(values)-1], 10, 32)
	if err != nil {
		return nil, err
	}
	return starlark.MakeInt64(v), nil
}

type labelBuildSettingCanonicalizer[TReference any, TMetadata model_core.CloneableReferenceMetadata] struct {
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
func NewLabelBuildSettingCanonicalizer[TReference any, TMetadata model_core.CloneableReferenceMetadata](currentPackage pg_label.CanonicalPackage, singletonList bool) BuildSettingCanonicalizer {
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

func (bsc *labelBuildSettingCanonicalizer[TReference, TMetadata]) CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (starlark.Value, error) {
	apparentLabel, err := bsc.currentPackage.AppendLabel(values[len(values)-1])
	if err != nil {
		return nil, err
	}
	resolvedLabel, err := pg_label.Resolve(labelResolver, bsc.currentPackage.GetCanonicalRepo(), apparentLabel)
	if err != nil {
		return nil, err
	}

	var value starlark.Value = NewLabel[TReference, TMetadata](resolvedLabel)
	if bsc.singletonList {
		value = starlark.NewList([]starlark.Value{value})
	}
	return value, nil
}

type labelListBuildSettingType[TReference any, TMetadata model_core.CloneableReferenceMetadata] struct {
	repeatable bool
}

func NewLabelListBuildSettingType[TReference any, TMetadata model_core.CloneableReferenceMetadata](repeatable bool) BuildSettingType {
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

type labelListBuildSettingCanonicalizer[TReference any, TMetadata model_core.CloneableReferenceMetadata] struct {
	unpack.Canonicalizer

	currentPackage pg_label.CanonicalPackage
	repeatable     bool
}

func (labelListBuildSettingCanonicalizer[TReference, TMetadata]) CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (starlark.Value, error) {
	return nil, errors.New("TODO: Implement canonicalization of config.label_list")
}

type stringBuildSettingType struct{}

var StringBuildSettingType BuildSettingType = stringBuildSettingType{}

func (stringBuildSettingType) Type() string {
	return "string"
}

func (stringBuildSettingType) Encode(out *model_starlark_pb.BuildSetting) {
	out.Type = &model_starlark_pb.BuildSetting_String_{
		String_: &emptypb.Empty{},
	}
}

func (stringBuildSettingType) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) BuildSettingCanonicalizer {
	return &stringBuildSettingCanonicalizer{
		Canonicalizer: unpack.String,
	}
}

type stringBuildSettingCanonicalizer struct {
	unpack.Canonicalizer
}

func (stringBuildSettingCanonicalizer) CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (starlark.Value, error) {
	return starlark.String(values[len(values)-1]), nil
}

type stringListBuildSettingType struct {
	repeatable bool
}

func NewStringListBuildSettingType(repeatable bool) BuildSettingType {
	return stringListBuildSettingType{
		repeatable: repeatable,
	}
}

func (stringListBuildSettingType) Type() string {
	return "string_list"
}

func (bst stringListBuildSettingType) Encode(out *model_starlark_pb.BuildSetting) {
	out.Type = &model_starlark_pb.BuildSetting_StringList{
		StringList: &model_starlark_pb.BuildSetting_ListType{
			Repeatable: bst.repeatable,
		},
	}
}

func (bst stringListBuildSettingType) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) BuildSettingCanonicalizer {
	return &stringListBuildSettingCanonicalizer{
		Canonicalizer: unpack.List(unpack.String),
		repeatable:    bst.repeatable,
	}
}

type stringListBuildSettingCanonicalizer struct {
	unpack.Canonicalizer
	repeatable bool
}

func (bsc stringListBuildSettingCanonicalizer) CanonicalizeStringList(values []string, labelResolver pg_label.Resolver) (starlark.Value, error) {
	if !bsc.repeatable {
		if lastValue := values[len(values)-1]; lastValue == "" {
			values = nil
		} else {
			values = strings.Split(lastValue, ",")
		}
	}

	elements := make([]starlark.Value, 0, len(values))
	for _, value := range values {
		elements = append(elements, starlark.String(value))
	}
	return starlark.NewList(elements), nil
}
