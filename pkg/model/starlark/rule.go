package starlark

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"bonanza.build/pkg/glob"
	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/eval/unpack"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
)

type rule[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	LateNamedValue
	definition RuleDefinition[TReference, TMetadata]
}

var (
	_ eval.Callable                                                       = (*rule[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*rule[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ NamedGlobal                                                         = (*rule[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewRule creates a Starlark rule object. These are normally created
// using the rule() function.
func NewRule[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](identifier *pg_label.CanonicalStarlarkIdentifier, definition RuleDefinition[TReference, TMetadata]) eval.Value {
	return &rule[TReference, TMetadata]{
		LateNamedValue: LateNamedValue{
			Identifier: identifier,
		},
		definition: definition,
	}
}

// Repr writes the representation of the value.
func (rule[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<rule>")
	return nil
}

func (rule[TReference, TMetadata]) Type() string {
	return "rule"
}

func (rule[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (r *rule[TReference, TMetadata]) Name() string {
	if r.Identifier == nil {
		return "rule"
	}
	return r.Identifier.GetStarlarkIdentifier().String()
}

// TargetRegistrarKey is a key for looking up an instance of
// TargetRegistrar that is part of the thread local variables of a
// Starlark thread. The TargetRegistrar is used by rule objects to
// register targets when a rule is invoked as part of a BUILD file.
const TargetRegistrarKey = "target_registrar"

func (r *rule[TReference, TMetadata]) CallInternal(thread *eval.Thread, args eval.Args) (eval.Value, error) {
	if len(args.Positional) != 0 {
		return nil, fmt.Errorf("got %d positional arguments, want 1", len(args.Positional))
	}

	if r.Identifier == nil {
		return nil, errors.New("rule does not have a name")
	}
	targetRegistrarValue := thread.Local(TargetRegistrarKey)
	if targetRegistrarValue == nil {
		return nil, fmt.Errorf("rule cannot be invoked from within this context")
	}
	targetRegistrar := targetRegistrarValue.(*TargetRegistrar[TReference, TMetadata])

	attrs, err := r.definition.GetAttrsCheap(thread)
	if err != nil {
		return nil, err
	}

	buildSetting, err := r.definition.GetBuildSetting(thread)
	if err != nil {
		return nil, err
	}

	test, err := r.definition.GetTest(thread)
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
		if name.IsPublic() {
			nameStr := name.String()
			switch nameStr {
			case "applicable_licenses", "compatible_with", "deprecation",
				"exec_compatible_with", "features", "name",
				"package_metadata", "tags", "target_compatible_with",
				"testonly", "visibility":
				return nil, fmt.Errorf("rule uses attribute with reserved name %#v", nameStr)
			case "build_setting_default", "help":
				if buildSetting != nil {
					return nil, fmt.Errorf("rule uses attribute with name %#v, which is reserved for build settings", nameStr)
				}
			case "args", "flaky", "local", "shard_count", "size", "timeout":
				if test {
					return nil, fmt.Errorf("rule uses attribute with name %#v, which is reserved for tests", nameStr)
				}
			}

			attr := attrs[name]
			filenameTemplate, isOutput := attr.attrType.IsOutput()
			if isOutput && filenameTemplate != "" {
				// Predeclared output declared using
				// rule(outputs = ...). These are not taken as
				// explicit arguments.
			} else {
				if attr.defaultValue == nil {
					// Attribute is mandatory.
					mandatoryUnpackers = append(mandatoryUnpackers, nameStr, &values[len(attrNames)])
				} else {
					// Attribute is optional.
					optionalUnpackers = append(optionalUnpackers, nameStr+"?", &values[len(attrNames)])
				}
			}

			attrNames = append(attrNames, name)
		} else {
			// TODO: We should also add all private label
			// types here, regardless of whether they are
			// public. This is needed to ensure
			// VisitLabels() is called properly.
		}
	}

	defaultInheritableAttrs := targetRegistrar.defaultInheritableAttrs.Message

	var name string
	mandatoryUnpackers = append(
		mandatoryUnpackers,
		"name", unpack.Bind(&name, unpack.Stringer(unpack.TargetName)),
	)

	var applicableLicenses []string
	var compatibleWith []string
	deprecation := defaultInheritableAttrs.Deprecation
	var execCompatibleWith []string
	var features *Select[TReference, TMetadata]
	packageMetadata := defaultInheritableAttrs.PackageMetadata
	var tags []string
	var targetCompatibleWith *Select[TReference, TMetadata]
	testOnly := defaultInheritableAttrs.Testonly
	visibility := targetRegistrar.getOmittedVisibility()
	labelUnpackerInto := NewLabelOrStringUnpackerInto[TReference, TMetadata](currentPackage)
	labelStringListUnpackerInto := unpack.List(unpack.Stringer(labelUnpackerInto))
	optionalUnpackers = append(
		optionalUnpackers,
		"applicable_licenses?", unpack.Bind(&applicableLicenses, unpack.IfNotNone(labelStringListUnpackerInto)),
		"compatible_with?", unpack.Bind(&compatibleWith, unpack.IfNotNone(labelStringListUnpackerInto)),
		"deprecation?", unpack.Bind(&deprecation, unpack.IfNotNone(unpack.String)),
		"exec_compatible_with?", unpack.Bind(&execCompatibleWith, unpack.IfNotNone(labelStringListUnpackerInto)),
		"features?", unpack.Bind(&features, unpack.IfNotNone(NewSelectUnpackerInto[TReference, TMetadata](unpack.Canonicalize(unpack.List(unpack.String))))),
		"package_metadata?", unpack.Bind(&packageMetadata, unpack.IfNotNone(labelStringListUnpackerInto)),
		"tags?", unpack.Bind(&tags, unpack.IfNotNone(unpack.List(unpack.String))),
		"target_compatible_with?", unpack.Bind(&targetCompatibleWith, unpack.IfNotNone(NewSelectUnpackerInto[TReference, TMetadata](unpack.Canonicalize(unpack.List(labelUnpackerInto))))),
		"testonly?", unpack.Bind(&testOnly, unpack.IfNotNone(sloppyBoolUnpackerInto{})),
		"visibility?", unpack.Bind(&visibility, targetRegistrar.newVisibilityUnpackerInto(currentPackage)),
	)

	var buildSettingDefault eval.Value
	if buildSetting != nil {
		var help string
		mandatoryUnpackers = append(
			mandatoryUnpackers,
			"build_setting_default",
			unpack.Bind(
				&buildSettingDefault,
				unpack.Canonicalize(buildSetting.buildSettingType.GetCanonicalizer(currentPackage)),
			),
		)
		optionalUnpackers = append(optionalUnpackers, "help?", unpack.Bind(&help, unpack.IfNotNone(unpack.String)))
	}

	// Attributes that are specific to tests.
	var testArgs *Select[TReference, TMetadata]
	testFlaky := false
	testLocal := false
	var testShardCount *Select[TReference, TMetadata]
	testSize := "medium"
	var testTimeout string
	if test {
		optionalUnpackers = append(
			optionalUnpackers,
			"args?",
			unpack.Bind(&testArgs, unpack.IfNotNone(NewSelectUnpackerInto[TReference, TMetadata](unpack.Canonicalize(unpack.List(unpack.String))))),
			"flaky?",
			unpack.Bind(&testFlaky, unpack.IfNotNone(sloppyBoolUnpackerInto{})),
			"local?",
			unpack.Bind(&testLocal, unpack.IfNotNone(sloppyBoolUnpackerInto{})),
			"shard_count?",
			unpack.Bind(&testShardCount, unpack.IfNotNone(NewSelectUnpackerInto[TReference, TMetadata](unpack.Canonicalize(unpack.Int[int]())))),
			"size?",
			unpack.Bind(&testSize, unpack.IfNotNone(unpack.String)),
			"timeout?",
			unpack.Bind(&testTimeout, unpack.IfNotNone(unpack.String)),
		)
	}

	if err := eval.UnpackArgs(
		thread, r.Identifier.GetStarlarkIdentifier().String(), args,
		append(mandatoryUnpackers, optionalUnpackers...)...,
	); err != nil {
		return nil, err
	}

	initializer, err := r.definition.GetInitializer(thread)
	if err != nil {
		return nil, err
	}
	if initializer != nil {
		initializerKwargs := make([]eval.NamedArg, 0, 1+len(attrNames))
		initializerKwargs = append(initializerKwargs, eval.NamedArg{
			Name:  "name",
			Value: eval.String(name),
		})
		for i, attrName := range attrNames {
			if v := values[i]; v != nil {
				initializerKwargs = append(initializerKwargs, eval.NamedArg{
					Name:  attrName.String(),
					Value: v,
				})
			}
		}
		overrides, err := eval.Call(thread, initializer, eval.Args{Named: initializerKwargs})
		if err != nil {
			return nil, fmt.Errorf("failed to run initializer: %w", err)
		}
		var overrideEntries map[string]eval.Value
		if err := unpack.Dict(unpack.String, unpack.Any).UnpackInto(thread, overrides, &overrideEntries); err != nil {
			return nil, fmt.Errorf("failed to unpack initializer return value: %w", err)
		}
		for name, value := range overrideEntries {
			if name == "name" {
				// Overriding "name" is not permitted.
				continue
			}
			index, ok := sort.Find(
				len(attrNames),
				func(i int) int { return strings.Compare(name, attrNames[i].String()) },
			)
			if !ok {
				return nil, fmt.Errorf("initializer returned value for attr %#v, which does not exist", name)
			}
			values[index] = value
		}
	}

	publicAttrValues := make([]*model_starlark_pb.RuleTarget_PublicAttrValue, 0, len(attrNames))
	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
	valueEncodingOptions := thread.Local(ValueEncodingOptionsKey).(*ValueEncodingOptions[TReference, TMetadata])
	for i, attrName := range attrNames {
		attr := attrs[attrName]
		value := values[i]

		// If the attr is a predeclared output declared using
		// rule(outputs = ...), compute its actual value.
		filenameTemplate, isOutput := attr.attrType.IsOutput()
		if isOutput && filenameTemplate != "" {
			targetName, err := pg_label.NewTargetName(strings.ReplaceAll(filenameTemplate, "%{name}", name))
			if err != nil {
				return nil, fmt.Errorf("invalid target name for predeclared output %#v with filename template %#v: %w", attrName, filenameTemplate, err)
			}
			value = NewLabel[TReference, TMetadata](currentPackage.AppendTargetName(targetName).AsResolved())
		}

		if value == nil {
			value = eval.None
		}
		var selectValue *Select[TReference, TMetadata]
		if err := attr.getSelectUnpackerInto(currentPackage).UnpackInto(thread, value, &selectValue); err != nil {
			return nil, fmt.Errorf("invalid argument %#v: %w", attrName.String(), err)
		}

		encodedGroups, err := selectValue.EncodeGroups(
			thread,
			/* path = */ nil,
			valueEncodingOptions,
		)
		if err != nil {
			return nil, err
		}
		publicAttrValues = append(
			publicAttrValues,
			&model_starlark_pb.RuleTarget_PublicAttrValue{
				ValueParts: encodedGroups.Message,
			},
		)
		patcher.Merge(encodedGroups.Patcher)

		// TODO: Visit the default value in case no explicit
		// value is set.
		// TODO: Also visit default values of private attributes.
		if _, isOutput := attr.attrType.IsOutput(); isOutput {
			// Register all labels as predeclared output targets.
			if err := selectValue.VisitLabels(thread, nil, func(l pg_label.ResolvedLabel) error {
				if canonicalLabel, err := l.AsCanonical(); err == nil {
					if canonicalLabel.GetCanonicalPackage() == currentPackage {
						targetName := canonicalLabel.GetTargetName().String()
						if err := targetRegistrar.registerExplicitTarget(
							targetName,
							model_core.NewPatchedMessage(
								&model_starlark_pb.Target_Definition{
									Kind: &model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget{
										PredeclaredOutputFileTarget: &model_starlark_pb.PredeclaredOutputFileTarget{
											OwnerTargetName: name,
										},
									},
								},
								patcher,
							),
						); err != nil {
							return err
						}
					}
				}
				return nil
			}); err != nil {
				return nil, err
			}
		} else {
			// Register all labels as implicit source file targets.
			if err := selectValue.VisitLabels(thread, nil, func(l pg_label.ResolvedLabel) error {
				if canonicalLabel, err := l.AsCanonical(); err == nil {
					if canonicalLabel.GetCanonicalPackage() == currentPackage {
						targetRegistrar.registerImplicitTarget(l.GetTargetName().String())
					}
				}
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}

	var encodedBuildSettingDefault model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]
	if buildSetting != nil {
		encodedBuildSettingDefault, err = EncodeValue[TReference, TMetadata](
			thread,
			buildSettingDefault,
			/* path = */ nil,
			/* identifier = */ nil,
			valueEncodingOptions,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to encode \"build_setting_default\": %w", err)
		}
		patcher.Merge(encodedBuildSettingDefault.Patcher)
	}

	if len(applicableLicenses) > 0 {
		if len(packageMetadata) > 0 {
			return nil, fmt.Errorf("\"applicable_licenses\" and \"package_metadata\" cannot be specified at the same time")
		}
		packageMetadata = applicableLicenses
	}

	if targetCompatibleWith == nil {
		targetCompatibleWith = NewSelect[TReference, TMetadata](
			[]SelectGroup{
				NewSelectGroup(nil, eval.NewList(nil), ""),
			},
			/* concatenationOperator = */ 0,
		)
	}
	targetCompatibleWithGroups, err := targetCompatibleWith.EncodeGroups(
		thread,
		/* path = */ nil,
		valueEncodingOptions,
	)
	if err != nil {
		return nil, err
	}
	patcher.Merge(targetCompatibleWithGroups.Patcher)

	sort.Strings(execCompatibleWith)
	sort.Strings(tags)

	visibilityPackageGroup, err := visibility.getTargetVisibility(targetRegistrar, thread, nil)
	if err != nil {
		return nil, err
	}
	patcher.Merge(visibilityPackageGroup.Patcher)

	var testMessage *model_starlark_pb.RuleTarget_Test
	if test {
		// Encode configurable attributes, using defaults if
		// they are not provided.
		if testArgs == nil {
			testArgs = NewSelect[TReference, TMetadata](
				[]SelectGroup{NewSelectGroup(nil, eval.NewList(nil), "")},
				/* concatenationOperator = */ 0,
			)
		}
		testArgsGroups, err := testArgs.EncodeGroups(
			thread,
			/* path = */ nil,
			valueEncodingOptions,
		)
		if err != nil {
			return nil, fmt.Errorf("attribute \"args\": %w", err)
		}
		patcher.Merge(testArgsGroups.Patcher)

		if testShardCount == nil {
			testShardCount = NewSelect[TReference, TMetadata](
				[]SelectGroup{NewSelectGroup(nil, eval.MakeInt(1), "")},
				/* concatenationOperator = */ 0,
			)
		}
		testShardCountGroups, err := testShardCount.EncodeGroups(
			thread,
			/* path = */ nil,
			valueEncodingOptions,
		)
		if err != nil {
			return nil, fmt.Errorf("attribute \"shard_count\": %w", err)
		}
		patcher.Merge(testShardCountGroups.Patcher)

		testMessage, err = newRuleTargetTest(testSize, testTimeout, testFlaky, testLocal, testShardCountGroups.Message, testArgsGroups.Message)
		if err != nil {
			return nil, err
		}
	}

	return eval.None, targetRegistrar.registerExplicitTarget(
		name,
		model_core.NewPatchedMessage(
			&model_starlark_pb.Target_Definition{
				Kind: &model_starlark_pb.Target_Definition_RuleTarget{
					RuleTarget: &model_starlark_pb.RuleTarget{
						RuleIdentifier:   r.Identifier.String(),
						PublicAttrValues: publicAttrValues,
						// TODO: Also set CompatibleWith. How is
						// it different from ExecCompatibleWith
						// and TargetWith?
						ExecCompatibleWith:   execCompatibleWith,
						Tags:                 slices.Compact(tags),
						TargetCompatibleWith: targetCompatibleWithGroups.Message,
						InheritableAttrs: &model_starlark_pb.InheritableAttrs{
							Deprecation:     deprecation,
							PackageMetadata: packageMetadata,
							Testonly:        testOnly,
							Visibility:      visibilityPackageGroup.Message,
						},
						BuildSettingDefault: encodedBuildSettingDefault.Message,
						Test:                testMessage,
					},
				},
			},
			patcher,
		),
	)
}

func (r *rule[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	if r.Identifier == nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, errors.New("rule does not have a name")
	}
	if currentIdentifier == nil || *currentIdentifier != *r.Identifier {
		// Not the canonical identifier under which this rule is
		// known. Emit a reference.
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Rule{
				Rule: &model_starlark_pb.Rule{
					Kind: &model_starlark_pb.Rule_Reference{
						Reference: r.Identifier.String(),
					},
				},
			},
		}), nil
	}

	definition, err := r.definition.Encode(thread, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Rule{
				Rule: &model_starlark_pb.Rule{
					Kind: &model_starlark_pb.Rule_Definition_{
						Definition: definition.Message,
					},
				},
			},
		},
		definition.Patcher,
	), nil
}

// RuleDefinition is used by Starlark rule objects to access the
// parameters that are part of the rule's definition, such as its
// attributes.
type RuleDefinition[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Rule_Definition, TMetadata], error)
	GetAttrsCheap(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error)
	GetBuildSetting(thread *eval.Thread) (*BuildSetting[TReference, TMetadata], error)
	GetInitializer(thread *eval.Thread) (eval.Function, error)
	GetTest(thread *eval.Thread) (bool, error)
}

type starlarkRuleDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	attrs          map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]
	buildSetting   *BuildSetting[TReference, TMetadata]
	cfg            TransitionDefinition[TReference, TMetadata]
	execGroups     map[string]*ExecGroup[TReference, TMetadata]
	implementation eval.Function
	initializer    eval.Function
	provides       []*Provider[TReference, TMetadata]
	ruleType       model_starlark_pb.Rule_Definition_Type
	subrules       []*Subrule[TReference, TMetadata]
}

// NewStarlarkRuleDefinition creates a RuleDefinition object that is
// backed by Starlark values. This is called when rule() is invoked in a
// .bzl file.
func NewStarlarkRuleDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	attrs map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata],
	buildSetting *BuildSetting[TReference, TMetadata],
	cfg TransitionDefinition[TReference, TMetadata],
	execGroups map[string]*ExecGroup[TReference, TMetadata],
	implementation eval.Function,
	initializer eval.Function,
	provides []*Provider[TReference, TMetadata],
	ruleType model_starlark_pb.Rule_Definition_Type,
	subrules []*Subrule[TReference, TMetadata],
) RuleDefinition[TReference, TMetadata] {
	return &starlarkRuleDefinition[TReference, TMetadata]{
		attrs:          attrs,
		buildSetting:   buildSetting,
		cfg:            cfg,
		execGroups:     execGroups,
		implementation: implementation,
		initializer:    initializer,
		provides:       provides,
		ruleType:       ruleType,
		subrules:       subrules,
	}
}

func (rd *starlarkRuleDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Rule_Definition, TMetadata], error) {
	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()

	var buildSetting *model_starlark_pb.BuildSetting
	if rd.buildSetting != nil {
		buildSetting = rd.buildSetting.Encode()
	}

	execGroups := make([]*model_starlark_pb.NamedExecGroup, 0, len(rd.execGroups))
	for _, name := range slices.Sorted(maps.Keys(rd.execGroups)) {
		execGroups = append(execGroups, &model_starlark_pb.NamedExecGroup{
			Name:      name,
			ExecGroup: rd.execGroups[name].Encode(),
		})
	}

	// Release all references that have been added to the patcher if
	// encoding fails.
	success := false
	defer func() {
		if !success {
			patcher.Discard()
		}
	}()

	implementation, err := EncodeFunction(thread, rd.implementation, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Rule_Definition, TMetadata]{}, err
	}
	implementationMessage := implementation.Merge(patcher)

	var initializerMessage *model_starlark_pb.Function
	if rd.initializer != nil {
		initializer, err := EncodeFunction(thread, rd.initializer, path, options)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Rule_Definition, TMetadata]{}, err
		}
		initializerMessage = initializer.Merge(patcher)
	}

	namedAttrs, err := encodeNamedAttrs(thread, rd.attrs, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Rule_Definition, TMetadata]{}, err
	}
	namedAttrsMessage := namedAttrs.Merge(patcher)

	cfgTransition, err := rd.cfg.EncodeUserDefinedTransition(thread, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Rule_Definition, TMetadata]{}, fmt.Errorf("failed to encode rule configuration transition: %w", err)
	}
	cfgTransitionMessage := cfgTransition.Merge(patcher)

	subruleIdentifiers := make([]string, 0, len(rd.subrules))
	for i, subrule := range rd.subrules {
		if subrule.Identifier == nil {
			return model_core.PatchedMessage[*model_starlark_pb.Rule_Definition, TMetadata]{}, fmt.Errorf("subrule at index %d does not have an identifier", i)
		}
		subruleIdentifiers = append(subruleIdentifiers, subrule.Identifier.String())
	}
	sort.Strings(subruleIdentifiers)
	success = true

	return model_core.NewPatchedMessage(
		&model_starlark_pb.Rule_Definition{
			Attrs:              namedAttrsMessage,
			BuildSetting:       buildSetting,
			CfgTransition:      cfgTransitionMessage,
			ExecGroups:         execGroups,
			Implementation:     implementationMessage,
			Initializer:        initializerMessage,
			Type:               rd.ruleType,
			SubruleIdentifiers: slices.Compact(subruleIdentifiers),
		},
		patcher,
	), nil
}

func (rd *starlarkRuleDefinition[TReference, TMetadata]) GetAttrsCheap(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	return rd.attrs, nil
}

func (rd *starlarkRuleDefinition[TReference, TMetadata]) GetBuildSetting(thread *eval.Thread) (*BuildSetting[TReference, TMetadata], error) {
	return rd.buildSetting, nil
}

func (rd *starlarkRuleDefinition[TReference, TMetadata]) GetInitializer(thread *eval.Thread) (eval.Function, error) {
	return rd.initializer, nil
}

func (rd *starlarkRuleDefinition[TReference, TMetadata]) GetTest(thread *eval.Thread) (bool, error) {
	return rd.ruleType == model_starlark_pb.Rule_Definition_TEST, nil
}

type protoRuleDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	message         model_core.Message[*model_starlark_pb.Rule_Definition, TReference]
	protoAttrsCache protoAttrsCache[TReference, TMetadata]
}

// NewProtoRuleDefinition creates a RuleDefinition object that loads all
// of the parameters that were used to construct the rule from a
// Protobuf message that was previously written to storage. This happens
// when a BUILD file contains a load() directive for a rule declared in
// a .bzl file.
func NewProtoRuleDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](message model_core.Message[*model_starlark_pb.Rule_Definition, TReference]) RuleDefinition[TReference, TMetadata] {
	return &protoRuleDefinition[TReference, TMetadata]{
		message: message,
	}
}

func (*protoRuleDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Rule_Definition, TMetadata], error) {
	panic("rule definition was already encoded previously")
}

func (rd *protoRuleDefinition[TReference, TMetadata]) GetAttrsCheap(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	return rd.protoAttrsCache.getAttrsCheap(thread, model_core.Nested(rd.message, rd.message.Message.Attrs))
}

func (rd *protoRuleDefinition[TReference, TMetadata]) GetBuildSetting(thread *eval.Thread) (*BuildSetting[TReference, TMetadata], error) {
	buildSettingMessage := rd.message.Message.BuildSetting
	if buildSettingMessage == nil {
		return nil, nil
	}
	return decodeBuildSetting[TReference, TMetadata](buildSettingMessage)
}

func (rd *protoRuleDefinition[TReference, TMetadata]) GetInitializer(thread *eval.Thread) (eval.Function, error) {
	if rd.message.Message.Initializer == nil {
		return nil, nil
	}
	return DecodeFunction[TReference, TMetadata](model_core.Nested(rd.message, rd.message.Message.Initializer)), nil
}

func (rd *protoRuleDefinition[TReference, TMetadata]) GetTest(thread *eval.Thread) (bool, error) {
	return rd.message.Message.Type == model_starlark_pb.Rule_Definition_TEST, nil
}

// getImplicitRuleAttrs returns the attributes that are accepted by
// rules in addition to the ones provided to rule(attrs = ...), with the
// exception of "name" and "visibility". These are used by
// macro(inherit_attrs = ...). The types and configurability of these
// attributes must match how calls to rules process the corresponding
// keyword arguments. None of them are mandatory.
func getImplicitRuleAttrs[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](test bool) map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata] {
	configurable := func(attrType AttrType[TReference, TMetadata]) *Attr[TReference, TMetadata] {
		return NewAttr(attrType, eval.None, true)
	}
	nonConfigurable := func(attrType AttrType[TReference, TMetadata]) *Attr[TReference, TMetadata] {
		return NewAttr(attrType, eval.None, false)
	}
	boolType := NewBoolAttrType[TReference, TMetadata]()
	labelListType := NewLabelListAttrType(
		glob.NFAMatchingNothing.Bytes(),
		NewProtoTransitionDefinition[TReference, TMetadata](model_core.NewSimpleMessage[TReference](&TargetTransition)),
	)
	stringType := NewStringAttrType[TReference, TMetadata](nil)
	stringListType := NewStringListAttrType[TReference, TMetadata]()

	attrs := map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]{
		util.Must(pg_label.NewStarlarkIdentifier("applicable_licenses")):    nonConfigurable(labelListType),
		util.Must(pg_label.NewStarlarkIdentifier("compatible_with")):        nonConfigurable(labelListType),
		util.Must(pg_label.NewStarlarkIdentifier("deprecation")):            nonConfigurable(stringType),
		util.Must(pg_label.NewStarlarkIdentifier("exec_compatible_with")):   nonConfigurable(labelListType),
		util.Must(pg_label.NewStarlarkIdentifier("features")):               configurable(stringListType),
		util.Must(pg_label.NewStarlarkIdentifier("package_metadata")):       nonConfigurable(labelListType),
		util.Must(pg_label.NewStarlarkIdentifier("tags")):                   nonConfigurable(stringListType),
		util.Must(pg_label.NewStarlarkIdentifier("target_compatible_with")): configurable(labelListType),
		util.Must(pg_label.NewStarlarkIdentifier("testonly")):               nonConfigurable(boolType),
	}
	if test {
		attrs[util.Must(pg_label.NewStarlarkIdentifier("args"))] = configurable(stringListType)
		attrs[util.Must(pg_label.NewStarlarkIdentifier("flaky"))] = nonConfigurable(boolType)
		attrs[util.Must(pg_label.NewStarlarkIdentifier("local"))] = nonConfigurable(boolType)
		attrs[util.Must(pg_label.NewStarlarkIdentifier("shard_count"))] = configurable(NewIntAttrType[TReference, TMetadata](nil))
		attrs[util.Must(pg_label.NewStarlarkIdentifier("size"))] = nonConfigurable(stringType)
		attrs[util.Must(pg_label.NewStarlarkIdentifier("timeout"))] = nonConfigurable(stringType)
	}
	return attrs
}

func (r *rule[TReference, TMetadata]) getInheritableAttrs(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	buildSetting, err := r.definition.GetBuildSetting(thread)
	if err != nil {
		return nil, err
	}
	test, err := r.definition.GetTest(thread)
	if err != nil {
		return nil, err
	}
	ruleAttrs, err := r.definition.GetAttrsCheap(thread)
	if err != nil {
		return nil, err
	}

	attrs := getImplicitRuleAttrs[TReference, TMetadata](test)
	if buildSetting != nil {
		// Build settings additionally require a default value
		// whose type matches that of the build setting. It
		// cannot be configurable, as it is evaluated prior to
		// configuration.
		attrs[util.Must(pg_label.NewStarlarkIdentifier("build_setting_default"))] = NewAttr(
			buildSetting.buildSettingType.GetAttrType(),
			/* defaultValue = */ nil,
			/* configurable = */ false,
		)
		attrs[util.Must(pg_label.NewStarlarkIdentifier("help"))] = NewAttr(
			NewStringAttrType[TReference, TMetadata](nil),
			eval.None,
			/* configurable = */ false,
		)
	}
	for name, attr := range ruleAttrs {
		// Predeclared outputs declared using rule(outputs = ...)
		// cannot be provided by the caller.
		if filenameTemplate, isOutput := attr.attrType.IsOutput(); name.IsPublic() && (!isOutput || filenameTemplate == "") {
			attrs[name] = attr
		}
	}
	return attrs, nil
}

type reloadingRuleDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	identifier pg_label.CanonicalStarlarkIdentifier
	base       atomic.Pointer[RuleDefinition[TReference, TMetadata]]
}

// NewReloadingRuleDefinition creates a RuleDefinition object for a rule
// for which only a canonical identifier is known, and the actual
// definition needs to be loaded lazily. This can occur when a BUILD
// file contains a load() directive for an identifier that is an alias
// for another rule (e.g., referring to an actual rule implementation
// inside a "private/" directory.
func NewReloadingRuleDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](identifier pg_label.CanonicalStarlarkIdentifier) RuleDefinition[TReference, TMetadata] {
	return &reloadingRuleDefinition[TReference, TMetadata]{
		identifier: identifier,
	}
}

func (*reloadingRuleDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Rule_Definition, TMetadata], error) {
	panic("rule definition was already encoded previously")
}

func (rd *reloadingRuleDefinition[TReference, TMetadata]) getBase(thread *eval.Thread) (RuleDefinition[TReference, TMetadata], error) {
	// TODO: Get rid of this caching. It has the downside that
	// dependencies are unstable.
	if base := rd.base.Load(); base != nil {
		return *base, nil
	}
	value, err := thread.Local(GlobalResolverKey).(GlobalResolver[TReference])(rd.identifier)
	if err != nil {
		return nil, err
	}

	valueKind, ok := value.Message.Kind.(*model_starlark_pb.Value_Rule)
	if !ok {
		return nil, fmt.Errorf("identifier %#v is not a rule", rd.identifier.String())
	}
	ruleKind, ok := valueKind.Rule.Kind.(*model_starlark_pb.Rule_Definition_)
	if !ok {
		return nil, fmt.Errorf("rule %#v does not have a definition", rd.identifier.String())
	}

	base := NewProtoRuleDefinition[TReference, TMetadata](model_core.Nested(value, ruleKind.Definition))
	rd.base.Store(&base)
	return base, nil
}

func (rd *reloadingRuleDefinition[TReference, TMetadata]) GetAttrsCheap(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	base, err := rd.getBase(thread)
	if err != nil {
		return nil, err
	}
	return base.GetAttrsCheap(thread)
}

func (rd *reloadingRuleDefinition[TReference, TMetadata]) GetBuildSetting(thread *eval.Thread) (*BuildSetting[TReference, TMetadata], error) {
	base, err := rd.getBase(thread)
	if err != nil {
		return nil, err
	}
	return base.GetBuildSetting(thread)
}

func (rd *reloadingRuleDefinition[TReference, TMetadata]) GetInitializer(thread *eval.Thread) (eval.Function, error) {
	base, err := rd.getBase(thread)
	if err != nil {
		return nil, err
	}
	return base.GetInitializer(thread)
}

func (rd *reloadingRuleDefinition[TReference, TMetadata]) GetTest(thread *eval.Thread) (bool, error) {
	base, err := rd.getBase(thread)
	if err != nil {
		return false, err
	}
	return base.GetTest(thread)
}

// bogusValue is a simple Starlark value type that acts as a
// placeholder. It can be used in places where we need to replace a
// value by a stub, and need to be sure the stub isn't being interpreted
// in any meaningful way.
type bogusValue struct{}

func (bogusValue) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<bogus_value>")
	return nil
}

func (bogusValue) Type() string {
	return "bogus_value"
}

func (bogusValue) Truth(*eval.Thread) (bool, error) {
	return false, nil
}

type protoAttrsCache[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	attrsCheap atomic.Pointer[map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]]
}

func (pac *protoAttrsCache[TReference, TMetadata]) getAttrsCheap(thread *eval.Thread, namedAttrs model_core.Message[[]*model_starlark_pb.NamedAttr, TReference]) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	if attrs := pac.attrsCheap.Load(); attrs != nil {
		return *attrs, nil
	}

	attrs := map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]{}
	for _, namedAttr := range namedAttrs.Message {
		name, err := pg_label.NewStarlarkIdentifier(namedAttr.Name)
		if err != nil {
			return nil, fmt.Errorf("attribute %#v: %w", namedAttr.Name, err)
		}
		if namedAttr.Attr == nil {
			return nil, fmt.Errorf("attribute %#v: missing message", namedAttr.Name)
		}
		attrType, err := DecodeAttrType[TReference, TMetadata](model_core.Nested(namedAttrs, namedAttr.Attr))
		if err != nil {
			return nil, fmt.Errorf("attribute %#v: %w", namedAttr.Name, err)
		}

		// Don't bother extracting the actual default value from
		// the rule. We don't need to know these in order to
		// determine if a rule is being called properly.
		var defaultValue eval.Value
		if namedAttr.Attr.Default != nil {
			defaultValue = bogusValue{}
		}

		attrs[name] = NewAttr[TReference, TMetadata](attrType, defaultValue, namedAttr.Attr.Configurable)
	}

	pac.attrsCheap.Store(&attrs)
	return attrs, nil
}
