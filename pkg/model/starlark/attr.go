package starlark

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/types/known/emptypb"
)

// Attr represents a Starlark rule attribute object. These are typically
// created by calling one of the attr.*() functions. They specify the
// type and behavior of a single attribute provided to a rule or
// repository rule.
type Attr[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	attrType     AttrType[TReference, TMetadata]
	defaultValue eval.Value
	configurable bool
}

var _ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*Attr[object.LocalReference, model_core.ReferenceMetadata])(nil)

// NewAttr creates a new Starlark rule attribute object. Each rule
// attribute has a certain type, and an optional default value that is
// used when the rule is called without specifying the attribute. If no
// default value is given, the attribute is mandatory. Only attributes
// of symbolic macros may be marked as not being configurable.
func NewAttr[TReference any, TMetadata model_core.ReferenceMetadata](attrType AttrType[TReference, TMetadata], defaultValue eval.Value, configurable bool) *Attr[TReference, TMetadata] {
	return &Attr[TReference, TMetadata]{
		attrType:     attrType,
		defaultValue: defaultValue,
		configurable: configurable,
	}
}

// getSelectUnpackerInto returns an unpacker for values of the
// attribute, converting them to select() objects. Values of attributes
// that are not configurable may not be provided using select().
func (a *Attr[TReference, TMetadata]) getSelectUnpackerInto(currentPackage pg_label.CanonicalPackage) unpack.UnpackerInto[*Select[TReference, TMetadata]] {
	canonicalizer := a.attrType.GetCanonicalizer(currentPackage)
	if a.defaultValue != nil {
		canonicalizer = unpack.IfNotNoneCanonicalizer(canonicalizer)
	}
	if !a.configurable {
		return NewNonConfigurableSelectUnpackerInto[TReference, TMetadata](canonicalizer)
	}
	return NewSelectUnpackerInto[TReference, TMetadata](canonicalizer)
}

// checkAttrsConfigurable returns an error if any of the provided
// attributes is marked as not being configurable. Apart from outputs,
// which are never configurable, this is only permitted for attributes
// of symbolic macros.
func checkAttrsConfigurable[TReference any, TMetadata model_core.ReferenceMetadata](attrs map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]) error {
	for _, name := range slices.SortedFunc(
		maps.Keys(attrs),
		func(a, b pg_label.StarlarkIdentifier) int { return strings.Compare(a.String(), b.String()) },
	) {
		if attr := attrs[name]; !attr.configurable {
			if _, isOutput := attr.attrType.IsOutput(); !isOutput {
				return fmt.Errorf("attr %#v: configurable may only be set to False for attributes of symbolic macros", name.String())
			}
		}
	}
	return nil
}

// clearDefaultValue returns a copy of the attribute whose default value
// is set to None. If the attribute is mandatory, it is returned as is.
func (a *Attr[TReference, TMetadata]) clearDefaultValue() *Attr[TReference, TMetadata] {
	if a.defaultValue == nil {
		return a
	}
	return NewAttr(a.attrType, eval.None, a.configurable)
}

// Repr writes the representation of a Starlark rule attribute object.
func (a *Attr[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString(fmt.Sprintf("<attr.%s>", a.attrType.Type()))
	return nil
}

// Type returns the type name of a Starlark rule attribute object in
// string form.
func (a *Attr[TReference, TMetadata]) Type() string {
	return "attr." + a.attrType.Type()
}

// Truth returns whether a Starlark rule attribute object is a "truthy"
// or a "falsy". Starlark rule attribute objects are always "truthy".
func (Attr[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

// Encode a Starlark rule attribute object, so that it can be written to
// storage.
func (a *Attr[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata], error) {
	attr, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_starlark_pb.Attr, error) {
		attr := model_starlark_pb.Attr{
			Configurable: a.configurable,
		}
		if a.defaultValue != nil {
			defaultValue, err := EncodeValue[TReference, TMetadata](thread, a.defaultValue, path, nil, options)
			if err != nil {
				return nil, err
			}
			attr.Default = defaultValue.Merge(patcher)
		}

		if err := a.attrType.Encode(thread, path, options, model_core.NewPatchedMessage(&attr, patcher)); err != nil {
			return nil, err
		}
		return &attr, nil
	})
	return attr, err
}

// EncodeValue encodes a Starlark rule attribute object to a generic
// Starlark value Protobuf message, so that it can be written to
// storage.
func (a *Attr[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	value, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_starlark_pb.Value, error) {
		attr, err := a.Encode(thread, path, options)
		if err != nil {
			return nil, err
		}

		return &model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Attr{
				Attr: attr.Merge(patcher),
			},
		}, nil
	})
	return value, err
}

// AttrType contains the properties of a rule attribute that are
// specific to the rule attribute's type.
type AttrType[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	Type() string
	Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error
	GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer
	IsOutput() (filenameTemplate string, ok bool)
}

// sloppyBoolUnpackerInto can be used to unpack Starlark Boolean values.
// For compatibility with Bazel, it also accepts integers with values
// zero and one, which it converts to False and True, respectively.
type sloppyBoolUnpackerInto struct{}

func (sloppyBoolUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *bool) error {
	if vInt, ok := v.(eval.Int); ok {
		if n, ok := vInt.Int64(); ok {
			switch n {
			case 0:
				*dst = false
				return nil
			case 1:
				*dst = true
				return nil
			}
		}
	}
	return unpack.Bool.UnpackInto(thread, v, dst)
}

func (ui sloppyBoolUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var b bool
	if err := ui.UnpackInto(thread, v, &b); err != nil {
		return nil, err
	}
	return eval.Bool(b), nil
}

func (sloppyBoolUnpackerInto) GetConcatenationOperator() syntax.Token {
	return 0
}

type boolAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct{}

// NewBoolAttrType creates a Boolean attribute type. These are normally
// constructed by calling config.bool().
func NewBoolAttrType[TReference any, TMetadata model_core.ReferenceMetadata]() AttrType[TReference, TMetadata] {
	return boolAttrType[TReference, TMetadata]{}
}

func (boolAttrType[TReference, TMetadata]) Type() string {
	return "bool"
}

func (boolAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	out.Message.Type = &model_starlark_pb.Attr_Bool{
		Bool: &emptypb.Empty{},
	}
	return nil
}

func (boolAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	return sloppyBoolUnpackerInto{}
}

func (boolAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return "", false
}

type intAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	values []int32
}

// NewIntAttrType creates an integer attribute type. These are normally
// constructed by calling config.int().
func NewIntAttrType[TReference any, TMetadata model_core.ReferenceMetadata](values []int32) AttrType[TReference, TMetadata] {
	return &intAttrType[TReference, TMetadata]{
		values: values,
	}
}

func (intAttrType[TReference, TMetadata]) Type() string {
	return "int"
}

func (at *intAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	out.Message.Type = &model_starlark_pb.Attr_Int{
		Int: &model_starlark_pb.Attr_IntType{
			Values: at.values,
		},
	}
	return nil
}

func (intAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	return unpack.Int[int32]()
}

func (intAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return "", false
}

type intListAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	allowEmpty bool
}

// NewIntListAttrType creates a list attribute type, where elements are
// integers. These are normally constructed by calling
// attr.int_list().
func NewIntListAttrType[TReference any, TMetadata model_core.ReferenceMetadata](allowEmpty bool) AttrType[TReference, TMetadata] {
	return &intListAttrType[TReference, TMetadata]{
		allowEmpty: allowEmpty,
	}
}

func (intListAttrType[TReference, TMetadata]) Type() string {
	return "int_list"
}

func (at *intListAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	out.Message.Type = &model_starlark_pb.Attr_IntList{
		IntList: &model_starlark_pb.Attr_IntListType{
			ListOptions: newCompositeOptions(at.allowEmpty),
		},
	}
	return nil
}

func (intListAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	return unpack.List(unpack.Int[int32]())
}

func (intListAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return "", false
}

// newCompositeOptions creates the options that apply to all attribute
// types containing dicts or lists.
func newCompositeOptions(allowEmpty bool) *model_starlark_pb.Attr_CompositeOptions {
	return &model_starlark_pb.Attr_CompositeOptions{
		AllowEmpty: allowEmpty,
	}
}

// GetAttrCompositeOptions returns the options of an attribute type
// containing a dict or list. If the attribute type does not contain a
// dict or list, false is returned.
func GetAttrCompositeOptions(attr *model_starlark_pb.Attr) (*model_starlark_pb.Attr_CompositeOptions, bool) {
	switch attrType := attr.GetType().(type) {
	case *model_starlark_pb.Attr_IntList:
		return attrType.IntList.GetListOptions(), true
	case *model_starlark_pb.Attr_LabelKeyedStringDict:
		return attrType.LabelKeyedStringDict.GetDictOptions(), true
	case *model_starlark_pb.Attr_LabelList:
		return attrType.LabelList.GetListOptions(), true
	case *model_starlark_pb.Attr_OutputList:
		return attrType.OutputList.GetListOptions(), true
	case *model_starlark_pb.Attr_StringDict:
		return attrType.StringDict.GetDictOptions(), true
	case *model_starlark_pb.Attr_StringKeyedLabelDict:
		return attrType.StringKeyedLabelDict.GetDictOptions(), true
	case *model_starlark_pb.Attr_StringList:
		return attrType.StringList.GetListOptions(), true
	case *model_starlark_pb.Attr_StringListDict:
		return attrType.StringListDict.GetDictOptions(), true
	default:
		return nil, false
	}
}

// LabelOptions contains the options that apply to all attribute types
// containing labels.
type LabelOptions[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	// Whether file targets are permitted, and the pattern of files
	// that referenced targets need to yield. If nil, no file targets
	// are permitted.
	AllowFiles *model_starlark_pb.Attr_AllowFiles

	// Identifiers of aspects that need to be applied to the targets
	// associated with the label, in the order in which they were
	// provided to attr.label(aspects = ...).
	Aspects []string

	// The transition to apply when configuring the target associated
	// with the label.
	Cfg TransitionDefinition[TReference, TMetadata]

	// Sets of providers that rule targets referenced by the attribute
	// need to provide, as returned by EncodeProviderSets.
	Providers []*model_starlark_pb.ProviderSet
}

// DecodeLabelOptions converts the options of an attribute type
// containing labels from its Protobuf message.
func DecodeLabelOptions[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](labelOptions model_core.Message[*model_starlark_pb.Attr_LabelOptions, TReference]) (LabelOptions[TReference, TMetadata], error) {
	if labelOptions.Message == nil || labelOptions.Message.Cfg == nil {
		return LabelOptions[TReference, TMetadata]{}, errors.New("missing label options")
	}
	return LabelOptions[TReference, TMetadata]{
		AllowFiles: labelOptions.Message.AllowFiles,
		Aspects:    labelOptions.Message.Aspects,
		Cfg: NewProtoTransitionDefinition[TReference, TMetadata](
			model_core.Nested(labelOptions, labelOptions.Message.Cfg),
		),
		Providers: labelOptions.Message.Providers,
	}, nil
}

func (lo *LabelOptions[TReference, TMetadata]) encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Attr_LabelOptions, TMetadata], error) {
	cfg, err := lo.Cfg.Encode(thread, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Attr_LabelOptions, TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Attr_LabelOptions{
			AllowFiles: lo.AllowFiles,
			Aspects:    lo.Aspects,
			Cfg:        cfg.Message,
			Providers:  lo.Providers,
		},
		cfg.Patcher,
	), nil
}

// getAttrLabelOptions returns the options of an attr type containing
// labels. If the attr type does not contain labels, false is returned.
func getAttrLabelOptions[TReference any, TMetadata model_core.ReferenceMetadata](attrType AttrType[TReference, TMetadata]) (*LabelOptions[TReference, TMetadata], bool) {
	switch typedAttrType := attrType.(type) {
	case *labelAttrType[TReference, TMetadata]:
		return &typedAttrType.valueOptions, true
	case *labelKeyedStringDictAttrType[TReference, TMetadata]:
		return &typedAttrType.dictKeyOptions, true
	case *labelListAttrType[TReference, TMetadata]:
		return &typedAttrType.listValueOptions, true
	case *stringKeyedLabelDictAttrType[TReference, TMetadata]:
		return &typedAttrType.dictValueOptions, true
	default:
		return nil, false
	}
}

type labelAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	allowNone       bool
	allowSingleFile bool
	executable      bool
	valueOptions    LabelOptions[TReference, TMetadata]
}

// NewLabelAttrType creates a label attribute type. These are normally
// constructed by calling config.label().
func NewLabelAttrType[TReference any, TMetadata model_core.ReferenceMetadata](allowNone, allowSingleFile, executable bool, valueOptions LabelOptions[TReference, TMetadata]) AttrType[TReference, TMetadata] {
	return &labelAttrType[TReference, TMetadata]{
		allowNone:       allowNone,
		allowSingleFile: allowSingleFile,
		executable:      executable,
		valueOptions:    valueOptions,
	}
}

func (labelAttrType[TReference, TMetadata]) Type() string {
	return "label"
}

func (at *labelAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	valueOptions, err := at.valueOptions.encode(thread, path, options)
	if err != nil {
		return err
	}
	out.Message.Type = &model_starlark_pb.Attr_Label{
		Label: &model_starlark_pb.Attr_LabelType{
			AllowNone:       at.allowNone,
			AllowSingleFile: at.allowSingleFile,
			Executable:      at.executable,
			ValueOptions:    valueOptions.Merge(out.Patcher),
		},
	}
	return nil
}

func (at *labelAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	canonicalizer := NewLabelOrStringUnpackerInto[TReference, TMetadata](currentPackage)
	if at.allowNone {
		canonicalizer = unpack.IfNotNone(canonicalizer)
	}
	return canonicalizer
}

func (labelAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return "", false
}

type labelKeyedStringDictAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	allowEmpty     bool
	dictKeyOptions LabelOptions[TReference, TMetadata]
}

// NewLabelKeyedStringDictAttrType creates a dictionary attribute type,
// where keys are labels and values are strings. These are normally
// constructed by calling config.string_keyed_label_dict().
func NewLabelKeyedStringDictAttrType[TReference any, TMetadata model_core.ReferenceMetadata](allowEmpty bool, dictKeyOptions LabelOptions[TReference, TMetadata]) AttrType[TReference, TMetadata] {
	return &labelKeyedStringDictAttrType[TReference, TMetadata]{
		allowEmpty:     allowEmpty,
		dictKeyOptions: dictKeyOptions,
	}
}

func (labelKeyedStringDictAttrType[TReference, TMetadata]) Type() string {
	return "label_keyed_string_dict"
}

func (at *labelKeyedStringDictAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	dictKeyOptions, err := at.dictKeyOptions.encode(thread, path, options)
	if err != nil {
		return err
	}
	out.Message.Type = &model_starlark_pb.Attr_LabelKeyedStringDict{
		LabelKeyedStringDict: &model_starlark_pb.Attr_LabelKeyedStringDictType{
			DictOptions:    newCompositeOptions(at.allowEmpty),
			DictKeyOptions: dictKeyOptions.Merge(out.Patcher),
		},
	}
	return nil
}

func (labelKeyedStringDictAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	return unpack.Dict(NewLabelOrStringUnpackerInto[TReference, TMetadata](currentPackage), unpack.String)
}

func (labelKeyedStringDictAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return "", false
}

type labelListAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	allowEmpty       bool
	listValueOptions LabelOptions[TReference, TMetadata]
}

// NewLabelListAttrType creates a list attribute type, where elements
// are labels. These are normally constructed by calling
// config.label_list().
func NewLabelListAttrType[TReference any, TMetadata model_core.ReferenceMetadata](allowEmpty bool, listValueOptions LabelOptions[TReference, TMetadata]) AttrType[TReference, TMetadata] {
	return &labelListAttrType[TReference, TMetadata]{
		allowEmpty:       allowEmpty,
		listValueOptions: listValueOptions,
	}
}

func (labelListAttrType[TReference, TMetadata]) Type() string {
	return "label_list"
}

func (at *labelListAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	listValueOptions, err := at.listValueOptions.encode(thread, path, options)
	if err != nil {
		return err
	}
	out.Message.Type = &model_starlark_pb.Attr_LabelList{
		LabelList: &model_starlark_pb.Attr_LabelListType{
			ListOptions:      newCompositeOptions(at.allowEmpty),
			ListValueOptions: listValueOptions.Merge(out.Patcher),
		},
	}
	return nil
}

func (labelListAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	return unpack.List(NewLabelOrStringUnpackerInto[TReference, TMetadata](currentPackage))
}

func (labelListAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return "", false
}

type outputAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	filenameTemplate string
}

// NewOutputAttrType creates an output file attribute type. These are
// normally constructed by calling config.output().
func NewOutputAttrType[TReference any, TMetadata model_core.ReferenceMetadata](filenameTemplate string) AttrType[TReference, TMetadata] {
	return &outputAttrType[TReference, TMetadata]{
		filenameTemplate: filenameTemplate,
	}
}

func (outputAttrType[TReference, TMetadata]) Type() string {
	return "output"
}

func (at *outputAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	out.Message.Type = &model_starlark_pb.Attr_Output{
		Output: &model_starlark_pb.Attr_OutputType{
			FilenameTemplate: at.filenameTemplate,
		},
	}
	return nil
}

func (outputAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	return NewLabelOrStringUnpackerInto[TReference, TMetadata](currentPackage)
}

func (at *outputAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return at.filenameTemplate, true
}

type outputListAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	allowEmpty bool
}

// NewOutputListAttrType creates a list attribute type, where elements
// are output files. These are normally constructed by calling
// config.output_list().
func NewOutputListAttrType[TReference any, TMetadata model_core.ReferenceMetadata](allowEmpty bool) AttrType[TReference, TMetadata] {
	return &outputListAttrType[TReference, TMetadata]{
		allowEmpty: allowEmpty,
	}
}

func (outputListAttrType[TReference, TMetadata]) Type() string {
	return "output_list"
}

func (at *outputListAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	out.Message.Type = &model_starlark_pb.Attr_OutputList{
		OutputList: &model_starlark_pb.Attr_OutputListType{
			ListOptions: newCompositeOptions(at.allowEmpty),
		},
	}
	return nil
}

func (outputListAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	return unpack.List(NewLabelOrStringUnpackerInto[TReference, TMetadata](currentPackage))
}

func (outputListAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return "", true
}

type stringAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	values []string
}

// NewStringAttrType creates a string attribute type. These are normally
// constructed by calling config.string().
func NewStringAttrType[TReference any, TMetadata model_core.ReferenceMetadata](values []string) AttrType[TReference, TMetadata] {
	return &stringAttrType[TReference, TMetadata]{
		values: values,
	}
}

func (stringAttrType[TReference, TMetadata]) Type() string {
	return "string"
}

func (at *stringAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	out.Message.Type = &model_starlark_pb.Attr_String_{
		String_: &model_starlark_pb.Attr_StringType{
			Values: at.values,
		},
	}
	return nil
}

func (stringAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	return unpack.String
}

func (stringAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return "", false
}

type stringDictAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	allowEmpty bool
}

// NewStringDictAttrType creates a dictionary attribute type, where keys
// and values are strings. These are normally constructed by calling
// config.string_dict().
func NewStringDictAttrType[TReference any, TMetadata model_core.ReferenceMetadata](allowEmpty bool) AttrType[TReference, TMetadata] {
	return &stringDictAttrType[TReference, TMetadata]{
		allowEmpty: allowEmpty,
	}
}

func (stringDictAttrType[TReference, TMetadata]) Type() string {
	return "string_dict"
}

func (at *stringDictAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	out.Message.Type = &model_starlark_pb.Attr_StringDict{
		StringDict: &model_starlark_pb.Attr_StringDictType{
			DictOptions: newCompositeOptions(at.allowEmpty),
		},
	}
	return nil
}

func (stringDictAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	return unpack.Dict(unpack.String, unpack.String)
}

func (stringDictAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return "", false
}

type stringListAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	allowEmpty bool
}

// NewStringListAttrType creates a list attribute type, where elements
// are strings. These are normally constructed by calling
// config.string_list().
func NewStringListAttrType[TReference any, TMetadata model_core.ReferenceMetadata](allowEmpty bool) AttrType[TReference, TMetadata] {
	return &stringListAttrType[TReference, TMetadata]{
		allowEmpty: allowEmpty,
	}
}

func (stringListAttrType[TReference, TMetadata]) Type() string {
	return "string_list"
}

func (at *stringListAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	out.Message.Type = &model_starlark_pb.Attr_StringList{
		StringList: &model_starlark_pb.Attr_StringListType{
			ListOptions: newCompositeOptions(at.allowEmpty),
		},
	}
	return nil
}

func (stringListAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	return unpack.List(unpack.String)
}

func (stringListAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return "", false
}

type stringKeyedLabelDictAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	allowEmpty       bool
	dictValueOptions LabelOptions[TReference, TMetadata]
}

// NewStringKeyedLabelDictAttrType creates a dictionary attribute type,
// where keys are labels and values are strings. These are normally
// constructed by calling config.string_keyed_label_dict().
func NewStringKeyedLabelDictAttrType[TReference any, TMetadata model_core.ReferenceMetadata](allowEmpty bool, dictValueOptions LabelOptions[TReference, TMetadata]) AttrType[TReference, TMetadata] {
	return &stringKeyedLabelDictAttrType[TReference, TMetadata]{
		allowEmpty:       allowEmpty,
		dictValueOptions: dictValueOptions,
	}
}

func (stringKeyedLabelDictAttrType[TReference, TMetadata]) Type() string {
	return "string_keyed_label_dict"
}

func (at *stringKeyedLabelDictAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	dictValueOptions, err := at.dictValueOptions.encode(thread, path, options)
	if err != nil {
		return err
	}
	out.Message.Type = &model_starlark_pb.Attr_StringKeyedLabelDict{
		StringKeyedLabelDict: &model_starlark_pb.Attr_StringKeyedLabelDictType{
			DictOptions:      newCompositeOptions(at.allowEmpty),
			DictValueOptions: dictValueOptions.Merge(out.Patcher),
		},
	}
	return nil
}

func (stringKeyedLabelDictAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	return unpack.Dict(unpack.String, NewLabelOrStringUnpackerInto[TReference, TMetadata](currentPackage))
}

func (stringKeyedLabelDictAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return "", false
}

type stringListDictAttrType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	allowEmpty bool
}

// NewStringListDictAttrType creates a dictionary attribute type, where
// keys are strings and values are lists of strings. These are normally
// constructed by calling config.string_list_dict().
func NewStringListDictAttrType[TReference any, TMetadata model_core.ReferenceMetadata](allowEmpty bool) AttrType[TReference, TMetadata] {
	return &stringListDictAttrType[TReference, TMetadata]{
		allowEmpty: allowEmpty,
	}
}

func (stringListDictAttrType[TReference, TMetadata]) Type() string {
	return "string_list_dict"
}

func (at *stringListDictAttrType[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata], out model_core.PatchedMessage[*model_starlark_pb.Attr, TMetadata]) error {
	out.Message.Type = &model_starlark_pb.Attr_StringListDict{
		StringListDict: &model_starlark_pb.Attr_StringListDictType{
			DictOptions: newCompositeOptions(at.allowEmpty),
		},
	}
	return nil
}

func (stringListDictAttrType[TReference, TMetadata]) GetCanonicalizer(currentPackage pg_label.CanonicalPackage) unpack.Canonicalizer {
	return unpack.Dict(unpack.String, unpack.List(unpack.String))
}

func (stringListDictAttrType[TReference, TMetadata]) IsOutput() (string, bool) {
	return "", false
}

func encodeNamedAttrs[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, attrs map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_starlark_pb.NamedAttr, TMetadata], error) {
	encodedAttrs := make([]*model_starlark_pb.NamedAttr, 0, len(attrs))
	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
	for _, name := range slices.SortedFunc(
		maps.Keys(attrs),
		func(a, b pg_label.StarlarkIdentifier) int { return strings.Compare(a.String(), b.String()) },
	) {
		attr, err := attrs[name].Encode(thread, path, options)
		if err != nil {
			patcher.Discard()
			return model_core.PatchedMessage[[]*model_starlark_pb.NamedAttr, TMetadata]{}, fmt.Errorf("attr %#v: %w", name, err)
		}
		encodedAttrs = append(encodedAttrs, &model_starlark_pb.NamedAttr{
			Name: name.String(),
			Attr: attr.Merge(patcher),
		})
	}
	return model_core.NewPatchedMessage(encodedAttrs, patcher), nil
}

// VisitAttrValueLabels calls a function for each label contained in
// the value of an attribute, as stored in a rule target, or as the
// default value of an attribute. As values of attributes are
// canonicalized to match the type of the attribute, only labels, lists
// and dicts can contain labels. Values that are functions (e.g.,
// computed default values of attributes) are ignored, as they cannot
// be evaluated without a configuration.
func VisitAttrValueLabels[TReference object.BasicReference](ctx context.Context, readers *ValueReaders[TReference], value model_core.Message[*model_starlark_pb.Value, TReference], visitor LabelVisitor) error {
	switch kind := value.Message.GetKind().(type) {
	case *model_starlark_pb.Value_Label:
		resolvedLabel, err := pg_label.NewResolvedLabel(kind.Label)
		if err != nil {
			return fmt.Errorf("invalid label %#v: %w", kind.Label, err)
		}
		return visitor(resolvedLabel)
	case *model_starlark_pb.Value_List:
		var errIter error
		for element := range AllListLeafElements(ctx, readers.List, model_core.Nested(value, kind.List.Elements), &errIter) {
			if err := VisitAttrValueLabels(ctx, readers, element, visitor); err != nil {
				return err
			}
		}
		return errIter
	case *model_starlark_pb.Value_Dict:
		var errIter error
		for key, value := range AllDictLeafEntries(ctx, readers.Dict, model_core.Nested(value, kind.Dict), &errIter) {
			if err := VisitAttrValueLabels(ctx, readers, key, visitor); err != nil {
				return err
			}
			if err := VisitAttrValueLabels(ctx, readers, value, visitor); err != nil {
				return err
			}
		}
		return errIter
	default:
		return nil
	}
}
