package starlark

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"
)

type macro[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	LateNamedValue
	definition MacroDefinition[TReference, TMetadata]
}

var (
	_ eval.Callable                                                       = (*macro[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*macro[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ NamedGlobal                                                         = (*macro[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewMacro creates a Starlark symbolic macro object. These are normally
// created using the macro() function.
func NewMacro[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](identifier *pg_label.CanonicalStarlarkIdentifier, definition MacroDefinition[TReference, TMetadata]) eval.Value {
	return &macro[TReference, TMetadata]{
		LateNamedValue: LateNamedValue{
			Identifier: identifier,
		},
		definition: definition,
	}
}

// Repr writes the representation of the value.
func (macro[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<macro>")
	return nil
}

func (macro[TReference, TMetadata]) Type() string {
	return "macro"
}

func (macro[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (m *macro[TReference, TMetadata]) Name() string {
	if m.Identifier == nil {
		return "macro"
	}
	return m.Identifier.GetStarlarkIdentifier().String()
}

// CallInternal is invoked when a symbolic macro is called from within a
// BUILD file or another symbolic macro. Instead of invoking the macro's
// implementation function immediately, the arguments of the call are
// recorded as a macro instance. The implementation function is only
// invoked when one of the targets declared by the macro is requested.
func (m *macro[TReference, TMetadata]) CallInternal(thread *eval.Thread, args eval.Args) (eval.Value, error) {
	if len(args.Positional) != 0 {
		return nil, fmt.Errorf("got %d positional arguments, want 0", len(args.Positional))
	}
	if m.Identifier == nil {
		return nil, errors.New("macro does not have a name")
	}
	targetRegistrarValue := thread.Local(TargetRegistrarKey)
	if targetRegistrarValue == nil {
		return nil, errors.New("macro cannot be invoked from within this context")
	}
	targetRegistrar := targetRegistrarValue.(*TargetRegistrar[TReference, TMetadata])

	finalizer, err := m.definition.IsFinalizer(thread)
	if err != nil {
		return nil, err
	}
	finalizerKind := model_starlark_pb.MacroInstance_NOT_A_FINALIZER
	if finalizer {
		finalizerKind = model_starlark_pb.MacroInstance_FINALIZER
	} else if targetRegistrar.macro != nil && targetRegistrar.macro.finalizer != model_starlark_pb.MacroInstance_NOT_A_FINALIZER {
		finalizerKind = model_starlark_pb.MacroInstance_CALLED_BY_FINALIZER
	}
	attrs, err := m.definition.GetAttrsCheap(thread)
	if err != nil {
		return nil, err
	}

	var mandatoryUnpackers, optionalUnpackers []any
	attrNames := make([]pg_label.StarlarkIdentifier, 0, len(attrs))
	values := make([]eval.Value, len(attrs))
	currentPackage := thread.Local(CanonicalPackageKey).(pg_label.CanonicalPackage)
	for _, name := range slices.SortedFunc(
		maps.Keys(attrs),
		func(a, b pg_label.StarlarkIdentifier) int { return strings.Compare(a.String(), b.String()) },
	) {
		if !name.IsPublic() {
			// Private attributes can't be set by the caller.
			// Their default values are provided to the
			// implementation function.
			continue
		}
		nameStr := name.String()
		switch nameStr {
		case "name", "visibility":
			return nil, fmt.Errorf("macro uses attribute with reserved name %#v", nameStr)
		}
		if attrs[name].defaultValue == nil {
			mandatoryUnpackers = append(mandatoryUnpackers, nameStr, &values[len(attrNames)])
		} else {
			optionalUnpackers = append(optionalUnpackers, nameStr+"?", &values[len(attrNames)])
		}
		attrNames = append(attrNames, name)
	}

	var name string
	visibility := targetRegistrar.getOmittedVisibility()
	mandatoryUnpackers = append(
		mandatoryUnpackers,
		// Names of macro instances are only used as a prefix of
		// the names of the targets and macro instances they
		// declare. They therefore don't need to be valid target
		// names.
		"name", unpack.Bind(&name, unpack.String),
	)
	optionalUnpackers = append(
		optionalUnpackers,
		"visibility?", unpack.Bind(&visibility, targetRegistrar.newVisibilityUnpackerInto(currentPackage)),
	)
	if err := eval.UnpackArgs(
		thread, m.Identifier.GetStarlarkIdentifier().String(), args,
		append(mandatoryUnpackers, optionalUnpackers...)...,
	); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, errors.New("macro instance name cannot be empty")
	}

	publicAttrValues := make([]*model_starlark_pb.RuleTarget_PublicAttrValue, 0, len(attrNames))
	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
	valueEncodingOptions := thread.Local(ValueEncodingOptionsKey).(*ValueEncodingOptions[TReference, TMetadata])
	for i, attrName := range attrNames {
		attr := attrs[attrName]
		value := values[i]
		if value == nil {
			value = eval.None
		}
		var selectValue *Select[TReference, TMetadata]
		if err := attr.getSelectUnpackerInto(currentPackage).UnpackInto(thread, value, &selectValue); err != nil {
			patcher.Discard()
			return nil, fmt.Errorf("invalid argument %#v: %w", attrName.String(), err)
		}
		encodedGroups, err := selectValue.EncodeGroups(
			thread,
			/* path = */ nil,
			valueEncodingOptions,
		)
		if err != nil {
			patcher.Discard()
			return nil, err
		}
		publicAttrValues = append(
			publicAttrValues,
			&model_starlark_pb.RuleTarget_PublicAttrValue{
				ValueParts: encodedGroups.Message,
			},
		)
		patcher.Merge(encodedGroups.Patcher)

		// Labels passed to the macro that refer to targets in
		// the current package need to be registered, as macros
		// can't declare source file targets themselves. If such
		// targets are in the namespace of a macro instance,
		// TargetRegistrar.Finalize() forwards them to it.
		if err := selectValue.VisitLabels(thread, nil, func(l pg_label.ResolvedLabel) error {
			if canonicalLabel, err := l.AsCanonical(); err == nil && canonicalLabel.GetCanonicalPackage() == currentPackage {
				targetRegistrar.registerImplicitTarget(canonicalLabel.GetTargetName().String())
			}
			return nil
		}); err != nil {
			patcher.Discard()
			return nil, err
		}
	}

	// The visibility that is provided to the macro's implementation
	// function implicitly includes the location from which the
	// macro is called.
	visibilityPackageGroup, err := visibility.getMacroInstanceVisibility(targetRegistrar, thread, currentPackage)
	if err != nil {
		patcher.Discard()
		return nil, err
	}
	patcher.Merge(visibilityPackageGroup.Patcher)

	defaultInheritableAttrs := targetRegistrar.defaultInheritableAttrs.Message
	return eval.None, targetRegistrar.registerMacroInstance(
		name,
		model_core.NewPatchedMessage(
			&model_starlark_pb.MacroInstance_Definition{
				MacroIdentifier:  m.Identifier.String(),
				PublicAttrValues: publicAttrValues,
				Finalizer:        finalizerKind,
				InheritableAttrs: &model_starlark_pb.InheritableAttrs{
					Deprecation:     defaultInheritableAttrs.GetDeprecation(),
					PackageMetadata: defaultInheritableAttrs.GetPackageMetadata(),
					Testonly:        defaultInheritableAttrs.GetTestonly(),
					Visibility:      visibilityPackageGroup.Message,
				},
			},
			patcher,
		),
	)
}

func (m *macro[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	if m.Identifier == nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, errors.New("macro does not have a name")
	}
	if currentIdentifier == nil || *currentIdentifier != *m.Identifier {
		// Not the canonical identifier under which this macro
		// is known. Emit a reference.
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Macro{
				Macro: &model_starlark_pb.Macro{
					Kind: &model_starlark_pb.Macro_Reference{
						Reference: m.Identifier.String(),
					},
				},
			},
		}), nil
	}

	definition, err := m.definition.Encode(thread, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Macro{
				Macro: &model_starlark_pb.Macro{
					Kind: &model_starlark_pb.Macro_Definition_{
						Definition: definition.Message,
					},
				},
			},
		},
		definition.Patcher,
	), nil
}

// inheritableAttrsSource is implemented by values that may be provided
// to macro(inherit_attrs = ...), namely rules, macros, None, and the
// string "common".
type inheritableAttrsSource[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	// getInheritableAttrs returns the attributes that a symbolic
	// macro may inherit. Only public attributes are returned.
	getInheritableAttrs(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error)
}

func (m *macro[TReference, TMetadata]) getInheritableAttrs(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	macroAttrs, err := m.definition.GetAttrsCheap(thread)
	if err != nil {
		return nil, err
	}
	attrs := make(map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], len(macroAttrs))
	for name, attr := range macroAttrs {
		if name.IsPublic() {
			attrs[name] = attr
		}
	}
	return attrs, nil
}

// noInheritableAttrs is used if macro() is called without
// inherit_attrs, or with inherit_attrs set to None, causing no
// attributes to be inherited.
type noInheritableAttrs[TReference any, TMetadata model_core.ReferenceMetadata] struct{}

func (noInheritableAttrs[TReference, TMetadata]) getInheritableAttrs(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	return nil, nil
}

// commonInheritableAttrs corresponds to
// macro(inherit_attrs = "common"), causing the attributes that are
// common to all rules to be inherited.
type commonInheritableAttrs[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct{}

func (commonInheritableAttrs[TReference, TMetadata]) getInheritableAttrs(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	return getImplicitRuleAttrs[TReference, TMetadata](
		/* test = */ false,
	), nil
}

type inheritableAttrsSourceUnpackerInto[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct{}

func (inheritableAttrsSourceUnpackerInto[TReference, TMetadata]) UnpackInto(thread *eval.Thread, v eval.Value, dst *inheritableAttrsSource[TReference, TMetadata]) error {
	switch typedV := v.(type) {
	case eval.String:
		if typedV != "common" {
			return fmt.Errorf("unknown set of attributes %#v", string(typedV))
		}
		*dst = commonInheritableAttrs[TReference, TMetadata]{}
	case inheritableAttrsSource[TReference, TMetadata]:
		*dst = typedV
	default:
		return fmt.Errorf("got %s, want rule, macro or \"common\"", v.Type())
	}
	return nil
}

func (ui inheritableAttrsSourceUnpackerInto[TReference, TMetadata]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var source inheritableAttrsSource[TReference, TMetadata]
	if err := ui.UnpackInto(thread, v, &source); err != nil {
		return nil, err
	}
	return v, nil
}

func (inheritableAttrsSourceUnpackerInto[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return 0
}

// MacroDefinition is used by Starlark macro objects to access the
// parameters that are part of the macro's definition, such as its
// attributes.
type MacroDefinition[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Macro_Definition, TMetadata], error)
	GetAttrsCheap(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error)
	IsFinalizer(thread *eval.Thread) (bool, error)
}

type starlarkMacroDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	attrs          map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]
	implementation eval.Function
	finalizer      bool
}

// NewStarlarkMacroDefinition creates a MacroDefinition object that is
// backed by Starlark values. This is called when macro() is invoked in
// a .bzl file.
func NewStarlarkMacroDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	attrs map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata],
	implementation eval.Function,
	finalizer bool,
) MacroDefinition[TReference, TMetadata] {
	return &starlarkMacroDefinition[TReference, TMetadata]{
		attrs:          attrs,
		implementation: implementation,
		finalizer:      finalizer,
	}
}

func (md *starlarkMacroDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Macro_Definition, TMetadata], error) {
	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
	implementation, err := EncodeFunction(thread, md.implementation, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Macro_Definition, TMetadata]{}, err
	}
	namedAttrs, err := encodeNamedAttrs(thread, md.attrs, path, options)
	if err != nil {
		implementation.Discard()
		return model_core.PatchedMessage[*model_starlark_pb.Macro_Definition, TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Macro_Definition{
			Attrs:          namedAttrs.Merge(patcher),
			Implementation: implementation.Merge(patcher),
			Finalizer:      md.finalizer,
		},
		patcher,
	), nil
}

func (md *starlarkMacroDefinition[TReference, TMetadata]) GetAttrsCheap(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	return md.attrs, nil
}

func (md *starlarkMacroDefinition[TReference, TMetadata]) IsFinalizer(thread *eval.Thread) (bool, error) {
	return md.finalizer, nil
}

type protoMacroDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	message         model_core.Message[*model_starlark_pb.Macro_Definition, TReference]
	protoAttrsCache protoAttrsCache[TReference, TMetadata]
}

// NewProtoMacroDefinition creates a MacroDefinition object that loads
// all of the parameters that were used to construct the macro from a
// Protobuf message that was previously written to storage. This happens
// when a BUILD file contains a load() directive for a macro declared in
// a .bzl file.
func NewProtoMacroDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](message model_core.Message[*model_starlark_pb.Macro_Definition, TReference]) MacroDefinition[TReference, TMetadata] {
	return &protoMacroDefinition[TReference, TMetadata]{
		message: message,
	}
}

func (*protoMacroDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Macro_Definition, TMetadata], error) {
	panic("macro definition was already encoded previously")
}

func (md *protoMacroDefinition[TReference, TMetadata]) GetAttrsCheap(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	return md.protoAttrsCache.getAttrsCheap(thread, model_core.Nested(md.message, md.message.Message.Attrs))
}

func (md *protoMacroDefinition[TReference, TMetadata]) IsFinalizer(thread *eval.Thread) (bool, error) {
	return md.message.Message.Finalizer, nil
}

type reloadingMacroDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	identifier pg_label.CanonicalStarlarkIdentifier
	base       atomic.Pointer[MacroDefinition[TReference, TMetadata]]
}

// NewReloadingMacroDefinition creates a MacroDefinition object for a
// macro for which only a canonical identifier is known, and the actual
// definition needs to be loaded lazily. This can occur when a BUILD
// file contains a load() directive for an identifier that is an alias
// for another macro.
func NewReloadingMacroDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](identifier pg_label.CanonicalStarlarkIdentifier) MacroDefinition[TReference, TMetadata] {
	return &reloadingMacroDefinition[TReference, TMetadata]{
		identifier: identifier,
	}
}

func (*reloadingMacroDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Macro_Definition, TMetadata], error) {
	panic("macro definition was already encoded previously")
}

func (md *reloadingMacroDefinition[TReference, TMetadata]) getBase(thread *eval.Thread) (MacroDefinition[TReference, TMetadata], error) {
	if base := md.base.Load(); base != nil {
		return *base, nil
	}
	value, err := thread.Local(GlobalResolverKey).(GlobalResolver[TReference])(md.identifier)
	if err != nil {
		return nil, err
	}
	definition, err := GetMacroDefinition(value)
	if err != nil {
		return nil, fmt.Errorf("identifier %#v: %w", md.identifier.String(), err)
	}
	base := NewProtoMacroDefinition[TReference, TMetadata](definition)
	md.base.Store(&base)
	return base, nil
}

func (md *reloadingMacroDefinition[TReference, TMetadata]) GetAttrsCheap(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	base, err := md.getBase(thread)
	if err != nil {
		return nil, err
	}
	return base.GetAttrsCheap(thread)
}

func (md *reloadingMacroDefinition[TReference, TMetadata]) IsFinalizer(thread *eval.Thread) (bool, error) {
	base, err := md.getBase(thread)
	if err != nil {
		return false, err
	}
	return base.IsFinalizer(thread)
}

// GetMacroDefinition extracts the definition of a macro from a
// Starlark value that was previously written to storage.
func GetMacroDefinition[TReference any](value model_core.Message[*model_starlark_pb.Value, TReference]) (model_core.Message[*model_starlark_pb.Macro_Definition, TReference], error) {
	valueKind, ok := value.Message.GetKind().(*model_starlark_pb.Value_Macro)
	if !ok {
		return model_core.Message[*model_starlark_pb.Macro_Definition, TReference]{}, errors.New("value is not a macro")
	}
	macroKind, ok := valueKind.Macro.Kind.(*model_starlark_pb.Macro_Definition_)
	if !ok {
		return model_core.Message[*model_starlark_pb.Macro_Definition, TReference]{}, errors.New("macro does not have a definition")
	}
	return model_core.Nested(value, macroKind.Definition), nil
}

// packageTargetName is the target name that is used to refer to all
// targets within a package in visibility specifications.
var packageTargetName = func() pg_label.TargetName {
	targetName, err := pg_label.NewTargetName("__pkg__")
	if err != nil {
		panic(err)
	}
	return targetName
}()

// IsValidMacroDeclarationName returns true if a target or macro
// instance declared by a macro instance has a name that is permitted.
// Names need to be equal to the name of the macro instance, or be
// prefixed by it, followed by "_", "-" or ".". This permits finding the
// macro instance that declares a target based on the target's name.
func IsValidMacroDeclarationName(macroName, name string) bool {
	suffix, ok := strings.CutPrefix(name, macroName)
	return ok && (suffix == "" || strings.ContainsRune("_-.", rune(suffix[0])))
}

// macroInstanceNameSBox maps the bytes in names of macro instances
// prior to comparing them. It causes "-", "." and "_" to sort before
// all other bytes, while retaining the relative order of these bytes
// among each other, and the relative order of all other bytes.
var macroInstanceNameSBox = func() (sBox [256]byte) {
	nextSeparator := byte(0)
	nextRegular := byte(3)
	for i := range sBox {
		switch i {
		case '-', '.', '_':
			sBox[i] = nextSeparator
			nextSeparator++
		default:
			sBox[i] = nextRegular
			nextRegular++
		}
	}
	return sBox
}()

// CompareMacroInstanceNames compares the names or name suffixes of
// macro instances, using the order in which macro expansion contexts
// are stored. In this order, the namespace of a macro instance is a
// contiguous range of names that starts at the name of the macro
// instance.
func CompareMacroInstanceNames(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return cmp.Compare(macroInstanceNameSBox[a[i]], macroInstanceNameSBox[b[i]])
		}
	}
	return cmp.Compare(len(a), len(b))
}

func computeMacroExpansionContextListParentNode[TMetadata model_core.ReferenceMetadata](createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_starlark_pb.MacroExpansionContext_Entry, object.LocalReference]) model_core.PatchedMessage[*model_starlark_pb.MacroExpansionContext_Entry, TMetadata] {
	var firstNameSuffix string
	switch firstEntry := childNodes.Message[0].Level.(type) {
	case *model_starlark_pb.MacroExpansionContext_Entry_Leaf:
		firstNameSuffix = firstEntry.Leaf.NameSuffix
	case *model_starlark_pb.MacroExpansionContext_Entry_Parent_:
		firstNameSuffix = firstEntry.Parent.FirstNameSuffix
	}
	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_starlark_pb.MacroExpansionContext_Entry {
		return &model_starlark_pb.MacroExpansionContext_Entry{
			Level: &model_starlark_pb.MacroExpansionContext_Entry_Parent_{
				Parent: &model_starlark_pb.MacroExpansionContext_Entry_Parent{
					Reference:       patcher.AddDecodableReference(createdObject),
					FirstNameSuffix: firstNameSuffix,
				},
			},
		}
	})
}

// NewMacroExpansionContextListBuilder creates a B-tree builder for
// lists of macro expansion contexts. Macro expansion contexts need to
// be provided in the order of CompareMacroInstanceNames().
func NewMacroExpansionContextListBuilder[TReference any, TMetadata model_core.ReferenceMetadata](options *ValueEncodingOptions[TReference, TMetadata]) btree.Builder[*model_starlark_pb.MacroExpansionContext_Entry, TMetadata] {
	return newBTreeBuilder(
		options,
		func(entry *model_starlark_pb.MacroExpansionContext_Entry) bool {
			return entry.GetParent() != nil
		},
		btree.Capturing(options.Context, options.ObjectCapturer, computeMacroExpansionContextListParentNode),
	)
}

func computeMacroInstanceListParentNode[TMetadata model_core.ReferenceMetadata](createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_starlark_pb.MacroInstance_Entry, object.LocalReference]) model_core.PatchedMessage[*model_starlark_pb.MacroInstance_Entry, TMetadata] {
	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_starlark_pb.MacroInstance_Entry {
		return &model_starlark_pb.MacroInstance_Entry{
			Level: &model_starlark_pb.MacroInstance_Entry_Parent_{
				Parent: &model_starlark_pb.MacroInstance_Entry_Parent{
					Reference: patcher.AddDecodableReference(createdObject),
				},
			},
		}
	})
}

// newMacroInstanceListBuilder creates a B-tree builder for lists of
// macro instances. Macro instances need to be provided in alphabetical
// order.
func newMacroInstanceListBuilder[TReference any, TMetadata model_core.ReferenceMetadata](options *ValueEncodingOptions[TReference, TMetadata]) btree.Builder[*model_starlark_pb.MacroInstance_Entry, TMetadata] {
	return newBTreeBuilder(
		options,
		func(entry *model_starlark_pb.MacroInstance_Entry) bool {
			return entry.GetParent() != nil
		},
		btree.Capturing(options.Context, options.ObjectCapturer, computeMacroInstanceListParentNode),
	)
}
