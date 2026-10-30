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
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/starlark"
)

type macro[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	LateNamedValue
	definition MacroDefinition[TReference, TMetadata]
}

var (
	_ starlark.Callable                                                   = (*macro[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*macro[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ NamedGlobal                                                         = (*macro[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewMacro creates a Starlark symbolic macro object. These are normally
// created using the macro() function.
func NewMacro[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](identifier *pg_label.CanonicalStarlarkIdentifier, definition MacroDefinition[TReference, TMetadata]) starlark.Value {
	return &macro[TReference, TMetadata]{
		LateNamedValue: LateNamedValue{
			Identifier: identifier,
		},
		definition: definition,
	}
}

func (macro[TReference, TMetadata]) String() string {
	return "<macro>"
}

func (macro[TReference, TMetadata]) Type() string {
	return "macro"
}

func (macro[TReference, TMetadata]) Freeze() {}

func (macro[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (macro[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("macro cannot be hashed")
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
func (m *macro[TReference, TMetadata]) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("got %d positional arguments, want 0", len(args))
	}
	if m.Identifier == nil {
		return nil, errors.New("macro does not have a name")
	}
	targetRegistrarValue := thread.Local(TargetRegistrarKey)
	if targetRegistrarValue == nil {
		return nil, errors.New("macro cannot be invoked from within this context")
	}
	targetRegistrar := targetRegistrarValue.(*TargetRegistrar[TReference, TMetadata])

	if err := m.definition.CheckSupported(thread); err != nil {
		return nil, err
	}
	attrs, err := m.definition.GetAttrsCheap(thread)
	if err != nil {
		return nil, err
	}

	var mandatoryUnpackers, optionalUnpackers []any
	attrNames := make([]pg_label.StarlarkIdentifier, 0, len(attrs))
	values := make([]starlark.Value, len(attrs))
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
		"name", unpack.Bind(thread, &name, unpack.String),
	)
	optionalUnpackers = append(
		optionalUnpackers,
		"visibility?", unpack.Bind(thread, &visibility, targetRegistrar.newVisibilityUnpackerInto(currentPackage)),
	)
	if err := starlark.UnpackArgs(
		m.Identifier.GetStarlarkIdentifier().String(), nil, kwargs,
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
			value = starlark.None
		}

		canonicalizer := attr.attrType.GetCanonicalizer(currentPackage)
		if attr.defaultValue != nil {
			canonicalizer = unpack.IfNotNoneCanonicalizer(canonicalizer)
		}
		var selectValue *Select[TReference, TMetadata]
		if err := NewSelectUnpackerInto[TReference, TMetadata](canonicalizer).UnpackInto(thread, value, &selectValue); err != nil {
			patcher.Discard()
			return nil, fmt.Errorf("invalid argument %#v: %w", attrName.String(), err)
		}
		encodedGroups, _, err := selectValue.EncodeGroups(
			/* path = */ map[starlark.Value]struct{}{},
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
		if err := selectValue.VisitLabels(thread, map[starlark.Value]struct{}{}, func(l pg_label.ResolvedLabel) error {
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
	return starlark.None, targetRegistrar.registerMacroInstance(
		name,
		model_core.NewPatchedMessage(
			&model_starlark_pb.MacroInstance_Definition{
				MacroIdentifier:  m.Identifier.String(),
				PublicAttrValues: publicAttrValues,
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

func (m *macro[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	if m.Identifier == nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, errors.New("macro does not have a name")
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
		}), false, nil
	}

	definition, needsCode, err := m.definition.Encode(path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
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
	), needsCode, nil
}

// MacroDefinition is used by Starlark macro objects to access the
// parameters that are part of the macro's definition, such as its
// attributes.
type MacroDefinition[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Macro_Definition, TMetadata], bool, error)
	GetAttrsCheap(thread *starlark.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error)
	CheckSupported(thread *starlark.Thread) error
}

// checkMacroSupported returns an error if a macro uses features that
// are not supported yet.
func checkMacroSupported(finalizer, inheritAttrs bool) error {
	if finalizer {
		return errors.New("macros that are finalizers are not supported yet")
	}
	if inheritAttrs {
		return errors.New("macros that inherit attributes are not supported yet")
	}
	return nil
}

type starlarkMacroDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	attrs          map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]
	implementation NamedFunction[TReference, TMetadata]
	finalizer      bool
	inheritAttrs   bool
}

// NewStarlarkMacroDefinition creates a MacroDefinition object that is
// backed by Starlark values. This is called when macro() is invoked in
// a .bzl file.
func NewStarlarkMacroDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	attrs map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata],
	implementation NamedFunction[TReference, TMetadata],
	finalizer bool,
	inheritAttrs bool,
) MacroDefinition[TReference, TMetadata] {
	return &starlarkMacroDefinition[TReference, TMetadata]{
		attrs:          attrs,
		implementation: implementation,
		finalizer:      finalizer,
		inheritAttrs:   inheritAttrs,
	}
}

func (md *starlarkMacroDefinition[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Macro_Definition, TMetadata], bool, error) {
	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
	implementation, needsCode, err := md.implementation.Encode(path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Macro_Definition, TMetadata]{}, false, err
	}
	namedAttrs, namedAttrsNeedCode, err := encodeNamedAttrs(md.attrs, path, options)
	if err != nil {
		implementation.Discard()
		return model_core.PatchedMessage[*model_starlark_pb.Macro_Definition, TMetadata]{}, false, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Macro_Definition{
			Attrs:          namedAttrs.Merge(patcher),
			Implementation: implementation.Merge(patcher),
			Finalizer:      md.finalizer,
			InheritAttrs:   md.inheritAttrs,
		},
		patcher,
	), needsCode || namedAttrsNeedCode, nil
}

func (md *starlarkMacroDefinition[TReference, TMetadata]) GetAttrsCheap(thread *starlark.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	return md.attrs, nil
}

func (md *starlarkMacroDefinition[TReference, TMetadata]) CheckSupported(thread *starlark.Thread) error {
	return checkMacroSupported(md.finalizer, md.inheritAttrs)
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

func (*protoMacroDefinition[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Macro_Definition, TMetadata], bool, error) {
	panic("macro definition was already encoded previously")
}

func (md *protoMacroDefinition[TReference, TMetadata]) GetAttrsCheap(thread *starlark.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	return md.protoAttrsCache.getAttrsCheap(thread, model_core.Nested(md.message, md.message.Message.Attrs))
}

func (md *protoMacroDefinition[TReference, TMetadata]) CheckSupported(thread *starlark.Thread) error {
	return checkMacroSupported(md.message.Message.Finalizer, md.message.Message.InheritAttrs)
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

func (*reloadingMacroDefinition[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Macro_Definition, TMetadata], bool, error) {
	panic("macro definition was already encoded previously")
}

func (md *reloadingMacroDefinition[TReference, TMetadata]) getBase(thread *starlark.Thread) (MacroDefinition[TReference, TMetadata], error) {
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

func (md *reloadingMacroDefinition[TReference, TMetadata]) GetAttrsCheap(thread *starlark.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	base, err := md.getBase(thread)
	if err != nil {
		return nil, err
	}
	return base.GetAttrsCheap(thread)
}

func (md *reloadingMacroDefinition[TReference, TMetadata]) CheckSupported(thread *starlark.Thread) error {
	base, err := md.getBase(thread)
	if err != nil {
		return err
	}
	return base.CheckSupported(thread)
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
