package analysis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"bonanza.build/pkg/label"
	model_command "bonanza.build/pkg/model/command"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func getAttrValueParts[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e getValueFromSelectGroupEnvironment[TReference, TMetadata],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	ruleTargetPackage label.CanonicalPackage,
	namedAttr model_core.Message[*model_starlark_pb.NamedAttr, TReference],
	publicAttrValue model_core.Message[*model_starlark_pb.RuleTarget_PublicAttrValue, TReference],
) (valueParts model_core.Message[[]*model_starlark_pb.Value, TReference], usedDefaultValue bool, err error) {
	if !strings.HasPrefix(namedAttr.Message.Name, "_") {
		// Attr is public. Extract the value from the rule target.
		selectGroups := publicAttrValue.Message.ValueParts
		if len(selectGroups) == 0 {
			return model_core.Message[[]*model_starlark_pb.Value, TReference]{}, false, fmt.Errorf("attr %#v has no select groups", namedAttr.Message.Name)
		}

		valueParts := make([]*model_starlark_pb.Value, 0, len(selectGroups))
		missingDependencies := false
		for _, selectGroup := range selectGroups {
			valuePart, err := getValueFromSelectGroup(
				e,
				configurationReference,
				ruleTargetPackage,
				selectGroup,
				false,
			)
			if err == nil {
				valueParts = append(valueParts, valuePart)
			} else if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
			} else {
				return model_core.Message[[]*model_starlark_pb.Value, TReference]{}, false, err
			}
		}
		if missingDependencies {
			return model_core.Message[[]*model_starlark_pb.Value, TReference]{}, false, evaluation.ErrMissingDependency
		}

		// Use the value from the rule target if it's not None.
		if len(valueParts) > 1 {
			return model_core.Nested(publicAttrValue, valueParts), false, nil
		}
		if _, ok := valueParts[0].Kind.(*model_starlark_pb.Value_None); !ok {
			return model_core.Nested(publicAttrValue, valueParts), false, nil
		}
	}

	// No value provided. Use the default value from the rule definition.
	defaultValue := namedAttr.Message.Attr.GetDefault()
	if defaultValue == nil {
		return model_core.Message[[]*model_starlark_pb.Value, TReference]{}, false, fmt.Errorf("missing value for mandatory attr %#v", namedAttr.Message.Name)
	}
	return model_core.Nested(namedAttr, []*model_starlark_pb.Value{defaultValue}), true, nil
}

func (c *baseComputer[TReference, TMetadata]) configureAttrValueParts(
	ctx context.Context,
	e ConfiguredTargetEnvironment[TReference, TMetadata],
	analysisEncoders *AnalysisEncoders[TReference],
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
	thread *eval.Thread,
	namedAttr model_core.Message[*model_starlark_pb.NamedAttr, TReference],
	valueParts model_core.Message[[]*model_starlark_pb.Value, TReference],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	visibilityFromPackage label.CanonicalPackage,
	execGroupPlatformLabels map[string]string,
) (eval.Value, error) {
	// See if any transitions need to be applied.
	var cfg *model_starlark_pb.Transition
	isScalar := false
	switch attrType := namedAttr.Message.Attr.GetType().(type) {
	case *model_starlark_pb.Attr_Label:
		cfg = attrType.Label.ValueOptions.GetCfg()
		isScalar = true
	case *model_starlark_pb.Attr_LabelKeyedStringDict:
		cfg = attrType.LabelKeyedStringDict.DictKeyOptions.GetCfg()
	case *model_starlark_pb.Attr_LabelList:
		cfg = attrType.LabelList.ListValueOptions.GetCfg()
	}

	var configurationReferences []model_core.Message[*model_core_pb.DecodableReference, TReference]
	mayHaveMultipleConfigurations := false
	if cfg != nil {
		var patchedResult model_core.PatchedMessage[*model_analysis_pb.UserDefinedTransition_Value_Success, TMetadata]
		var err error
		patchedResult, mayHaveMultipleConfigurations, err = c.performTransition(
			ctx,
			e,
			analysisEncoders,
			starlarkValueEncoders,
			model_core.Nested(namedAttr, cfg),
			configurationReference,
			model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
				// TODO!
			}),
			execGroupPlatformLabels,
		)
		if err != nil {
			return nil, err
		}
		result := model_core.Unpatch(e, patchedResult).Decay()
		for _, entry := range result.Message.Entries {
			configurationReferences = append(configurationReferences, model_core.Nested(result, entry.OutputConfigurationReference))
		}
	}

	var attrBuilder model_starlark.AttrValueBuilder
	if len(configurationReferences) == 0 {
		for _, valuePart := range valueParts.Message {
			decodedPart, err := model_starlark.DecodeAttrValuePart[TReference, TMetadata](
				thread, model_core.Nested(valueParts, valuePart),
				starlarkValueEncoders.newValueDecodingOptions(func(originalLabel label.ResolvedLabel) (eval.Value, error) {
					// We should leave the target
					// unconfigured. Provide a
					// target reference that does
					// not contain any providers.
					return model_starlark.NewTargetReference[TReference, TMetadata](
						originalLabel,
						/* configured = */ nil,
					), nil
				}),
			)
			if err != nil {
				return nil, err
			}
			if err := attrBuilder.Add(thread, decodedPart); err != nil {
				return nil, err
			}
		}
	} else {
		missingDependencies := false
		for _, configurationReference := range configurationReferences {
			valueDecodingOptions := starlarkValueEncoders.newValueDecodingOptions(func(originalLabel label.ResolvedLabel) (eval.Value, error) {
				// Resolve the label.
				canonicalOriginalLabel, err := originalLabel.AsCanonical()
				if err != nil {
					return nil, err
				}
				patchedConfigurationReference1 := model_core.Patch(e, configurationReference)
				resolvedLabelValue := e.GetVisibleTargetValue(
					model_core.NewPatchedMessage(
						&model_analysis_pb.VisibleTarget_Key{
							FromPackage:            visibilityFromPackage.String(),
							ToLabel:                canonicalOriginalLabel.String(),
							ConfigurationReference: patchedConfigurationReference1.Message,
						},
						patchedConfigurationReference1.Patcher,
					),
				)
				if !resolvedLabelValue.IsSet() {
					missingDependencies = true
					return eval.None, nil
				}
				resolvedLabelStr := resolvedLabelValue.Message.Label
				if resolvedLabelStr == "" {
					return eval.None, nil
				}
				resolvedLabel, err := label.NewCanonicalLabel(resolvedLabelStr)
				if err != nil {
					return nil, fmt.Errorf("invalid label %#v: %w", resolvedLabelStr, err)
				}

				// Obtain the providers of the target.
				patchedConfigurationReference2 := model_core.Patch(e, configurationReference)
				targetProviders := e.GetTargetProvidersValue(
					model_core.NewPatchedMessage(
						&model_analysis_pb.TargetProviders_Key{
							Label:                  resolvedLabelStr,
							ConfigurationReference: patchedConfigurationReference2.Message,
						},
						patchedConfigurationReference2.Patcher,
					),
				)
				if !targetProviders.IsSet() {
					missingDependencies = true
					return eval.None, nil
				}

				return model_starlark.NewTargetReference(
					originalLabel,
					model_starlark.NewConfiguredTargetReference[TReference, TMetadata](
						resolvedLabel,
						model_core.Nested(targetProviders, targetProviders.Message.ProviderInstances),
					),
				), nil
			})
			for _, valuePart := range valueParts.Message {
				decodedPart, err := model_starlark.DecodeAttrValuePart[TReference, TMetadata](
					thread, model_core.Nested(valueParts, valuePart),
					valueDecodingOptions,
				)
				if err != nil {
					return nil, err
				}
				if isScalar && mayHaveMultipleConfigurations {
					decodedPart, err = decodedPart.AsList(thread /* omitNone = */, false)
					if err != nil {
						return nil, err
					}
				}
				if err := attrBuilder.Add(thread, decodedPart); err != nil {
					return nil, err
				}
			}
		}
		if missingDependencies {
			return nil, evaluation.ErrMissingDependency
		}
	}

	attr, err := attrBuilder.Build(thread)
	if err != nil {
		return nil, err
	}
	if attr == nil {
		return nil, errors.New("attr value does not have any parts")
	}
	return attr, nil
}

type targetOutput[TMetadata model_core.ReferenceMetadata] struct {
	// Constant fields.
	packageRelativePath label.TargetName
	fileType            model_starlark_pb.File_Owner_Type

	// Variable fields.
	definition model_core.PatchedMessage[*model_analysis_pb.TargetOutputDefinition, TMetadata]
}

func (o *targetOutput[TMetadata]) setDefinition(definition model_core.PatchedMessage[*model_analysis_pb.TargetOutputDefinition, TMetadata]) error {
	if o.definition.IsSet() {
		return fmt.Errorf("file %#v is an output of multiple actions", o.packageRelativePath.String())
	}
	o.definition = definition
	return nil
}

type targetOutputRegistrar[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference]
	targetLabel            label.CanonicalLabel

	outputsByPackageRelativePath map[string]*targetOutput[TMetadata]
	outputsByFile                map[*model_starlark.File[TReference, TMetadata]]*targetOutput[TMetadata]
}

func (or *targetOutputRegistrar[TReference, TMetadata]) registerOutput(filename label.TargetName, sibling *model_starlark.File[TReference, TMetadata], fileType model_starlark_pb.File_Owner_Type) (eval.Value, error) {
	// If a sibling is provided, path resolution needs to start in
	// the directory containing containing the sibling.
	if sibling != nil {
		siblingLabelStr := sibling.GetDefinition().Message.Label
		siblingLabel, err := label.NewCanonicalLabel(siblingLabelStr)
		if err != nil {
			return nil, fmt.Errorf("invalid label for sibling %#v: %w", siblingLabelStr, err)
		}
		if siblingLabel.GetCanonicalPackage() != or.targetLabel.GetCanonicalPackage() {
			return nil, fmt.Errorf("sibling %#v is not declared in the same package", siblingLabel.String())
		}
		filename = siblingLabel.GetTargetName().GetSibling(filename)
	}

	o := &targetOutput[TMetadata]{
		packageRelativePath: filename,
		fileType:            fileType,
	}
	or.outputsByPackageRelativePath[filename.String()] = o
	f := model_starlark.NewFile[TReference, TMetadata](
		model_core.Nested(or.configurationReference, &model_starlark_pb.File{
			Owner: &model_starlark_pb.File_Owner{
				ConfigurationReference: or.configurationReference.Message,
				TargetName:             or.targetLabel.GetTargetName().String(),
				Type:                   fileType,
			},
			Label: or.targetLabel.GetCanonicalPackage().AppendTargetName(filename).String(),
		}),
	)
	or.outputsByFile[f] = o
	return f, nil
}

// targetOutputRegistrar is able to convert model_starlark.File objects
// that were declared as part of the current target to targetOutputs.
// This allows them to be associated to the action that yields them.
var _ unpack.UnpackerInto[*targetOutput[model_core.ReferenceMetadata]] = (*targetOutputRegistrar[object.BasicReference, model_core.ReferenceMetadata])(nil)

func (or *targetOutputRegistrar[TReference, TMetadata]) UnpackInto(thread *eval.Thread, v eval.Value, dst **targetOutput[TMetadata]) error {
	var f *model_starlark.File[TReference, TMetadata]
	if err := unpack.Type[*model_starlark.File[TReference, TMetadata]]("File").UnpackInto(thread, v, &f); err != nil {
		return err
	}
	o, ok := or.outputsByFile[f]
	if !ok {
		repr, err := eval.Repr(thread, f)
		if err != nil {
			return err
		}
		return fmt.Errorf("%s was not declared as part of this target", repr)
	}
	*dst = o
	return nil
}

func (or *targetOutputRegistrar[TReference, TMetadata]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var o *targetOutput[TMetadata]
	if err := or.UnpackInto(thread, v, &o); err != nil {
		return nil, err
	}
	return v, nil
}

func (targetOutputRegistrar[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return syntax.PLUS
}

type ruleContext[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	computer                    *baseComputer[TReference, TMetadata]
	context                     context.Context
	environment                 ConfiguredTargetEnvironment[TReference, TMetadata]
	valueEncodingOptionsFactory *valueEncodingOptionsFactory[TReference, TMetadata]
	starlarkValueEncoders       *StarlarkValueEncoders[TReference]
	analysisEncoders            *AnalysisEncoders[TReference]
	ruleIdentifier              label.CanonicalStarlarkIdentifier
	targetLabel                 label.CanonicalLabel
	configurationReference      model_core.Message[*model_core_pb.DecodableReference, TReference]
	ruleDefinition              model_core.Message[*model_starlark_pb.Rule_Definition, TReference]
	ruleTarget                  model_core.Message[*model_starlark_pb.RuleTarget, TReference]
	attr                        eval.Value
	splitAttr                   eval.Value
	buildSettingValue           eval.Value
	executable                  eval.Value
	executableFileToFilesToRun  map[*model_starlark.File[TReference, TMetadata]]model_core.Message[*model_starlark_pb.Struct, TReference]
	file                        eval.Value
	files                       eval.Value
	outputs                     eval.Value
	execGroups                  []ruleContextExecGroupState
	tags                        eval.List
	outputRegistrar             *targetOutputRegistrar[TReference, TMetadata]
	actionEncoder               model_encoding.DeterministicBinaryEncoder
	directoryCreationParameters *model_filesystem.DirectoryCreationParameters
	fileCreationParameters      *model_filesystem.FileCreationParameters
	actions                     model_core.PatchedMessageList[*model_analysis_pb.ConfiguredTarget_Value_Action_Leaf, TMetadata]
}

var _ eval.HasAttrs = (*ruleContext[object.GlobalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (rc *ruleContext[TReference, TMetadata]) Repr(_ *eval.Thread, printer *eval.Printer) error {
	printer.WriteString(fmt.Sprintf("<ctx for %s>", rc.targetLabel.String()))
	return nil
}

func (ruleContext[TReference, TMetadata]) Type() string {
	return "ctx"
}

func (ruleContext[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (rc *ruleContext[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	switch name {
	case "actions":
		return &ruleContextActions[TReference, TMetadata]{
			ruleContext: rc,
		}, nil
	case "attr":
		return rc.attr, nil
	case "bin_dir":
		configurationComponent, err := model_starlark.ConfigurationReferenceToComponent(rc.configurationReference)
		if err != nil {
			return nil, err
		}
		return model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
			"path": eval.String(model_starlark.ComponentStrBazelOut + "/" + configurationComponent + "/" + model_starlark.ComponentStrBin),
		}), nil
	case "build_setting_value":
		if rc.buildSettingValue == nil {
			buildSettingDefault := rc.ruleTarget.Message.BuildSettingDefault
			if buildSettingDefault == nil {
				return nil, eval.NoSuchAttrError{Value: rc, Name: name}
			}

			targetLabelStr := rc.targetLabel.String()
			override, err := btree.Find(
				rc.context,
				rc.analysisEncoders.Readers.BuildSettingOverride,
				getBuildSettingOverridesFromReference(rc.configurationReference),
				func(entry model_core.Message[*model_analysis_pb.BuildSettingOverride, TReference]) (int, *model_core_pb.DecodableReference) {
					switch level := entry.Message.Level.(type) {
					case *model_analysis_pb.BuildSettingOverride_Leaf_:
						return strings.Compare(targetLabelStr, level.Leaf.Label), nil
					case *model_analysis_pb.BuildSettingOverride_Parent_:
						return strings.Compare(targetLabelStr, level.Parent.FirstLabel), level.Parent.Reference
					default:
						return 0, nil
					}
				},
			)
			if err != nil {
				return nil, err
			}

			var encodedValue model_core.Message[*model_starlark_pb.Value, TReference]
			if override.IsSet() {
				overrideLeaf, ok := override.Message.Level.(*model_analysis_pb.BuildSettingOverride_Leaf_)
				if !ok {
					return nil, errors.New("build setting override is not a valid leaf")
				}
				encodedValue = model_core.Nested(override, overrideLeaf.Leaf.Value)
			} else {
				encodedValue = model_core.Nested(rc.ruleTarget, rc.ruleTarget.Message.BuildSettingDefault)
			}

			value, err := model_starlark.DecodeValue[TReference, TMetadata](thread, encodedValue, nil)
			if err != nil {
				return nil, err
			}
			rc.buildSettingValue = value
		}
		return rc.buildSettingValue, nil
	case "exec_groups":
		return &ruleContextExecGroups[TReference, TMetadata]{
			ruleContext: rc,
		}, nil
	case "executable":
		return rc.executable, nil
	case "file":
		return rc.file, nil
	case "files":
		return rc.files, nil
	case "info_file":
		// TODO: Fill all of this in properly.
		return model_starlark.NewFile[TReference, TMetadata](
			model_core.NewSimpleMessage[TReference](
				&model_starlark_pb.File{
					Owner: &model_starlark_pb.File_Owner{
						TargetName: "stamp",
						Type:       model_starlark_pb.File_Owner_FILE,
					},
					Label: "@@builtins_core+//:stable-status.txt",
				},
			),
		), nil
	case "label":
		return model_starlark.NewLabel[TReference, TMetadata](rc.targetLabel.AsResolved()), nil
	case "outputs":
		return rc.outputs, nil
	case "split_attr":
		return rc.splitAttr, nil
	case "version_file":
		// TODO: Fill all of this in properly.
		return model_starlark.NewFile[TReference, TMetadata](
			model_core.NewSimpleMessage[TReference](
				&model_starlark_pb.File{
					Owner: &model_starlark_pb.File_Owner{
						TargetName: "stamp",
						Type:       model_starlark_pb.File_Owner_FILE,
					},
					Label: "@@builtins_core+//:volatile-status.txt",
				},
			),
		), nil
	default:
		return nil, eval.NoSuchAttrError{Value: rc, Name: name}
	}
}

var ruleContextAttrNames = []string{
	"actions",
	"attr",
	"bin_dir",
	"exec_groups",
	"executable",
	"file",
	"files",
	"info_file",
	"label",
	"outputs",
	"split_attr",
	"version_file",
}

func (rc *ruleContext[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	attrNames := append([]string(nil), ruleContextAttrNames...)
	if rc.ruleTarget.Message.BuildSettingDefault != nil {
		attrNames = append(attrNames, "build_setting_value")
	}
	return attrNames, nil
}

func (rc *ruleContext[TReference, TMetadata]) setOutputToStaticDirectory(output *targetOutput[TMetadata], capturableDirectory model_filesystem.CapturableDirectory[TMetadata, TMetadata]) error {
	var createdDirectory model_filesystem.CreatedDirectory[TMetadata]
	group, groupCtx := errgroup.WithContext(rc.context)
	group.Go(func() error {
		return model_filesystem.CreateDirectoryMerkleTree(
			groupCtx,
			semaphore.NewWeighted(1),
			group,
			rc.directoryCreationParameters,
			capturableDirectory,
			model_filesystem.NewSimpleDirectoryMerkleTreeCapturer(rc.environment),
			&createdDirectory,
		)
	})
	if err := group.Wait(); err != nil {
		return err
	}

	return output.setDefinition(
		model_core.NewPatchedMessage(
			&model_analysis_pb.TargetOutputDefinition{
				Source: &model_analysis_pb.TargetOutputDefinition_StaticPackageDirectory{
					StaticPackageDirectory: createdDirectory.Message.Message,
				},
			},
			createdDirectory.Message.Patcher,
		),
	)
}

// getFileOrFilesToRunProvider takes a File or a FilesToRunProvider and
// returns it as a typed value.
func (rc *ruleContext[TReference, TMetadata]) getFileOrFilesToRunProvider(thread *eval.Thread, value any) (*model_starlark.File[TReference, TMetadata], *model_starlark.Struct[TReference, TMetadata], error) {
	switch typedValue := value.(type) {
	case *model_starlark.File[TReference, TMetadata]:
		if filesToRun, ok := rc.executableFileToFilesToRun[typedValue]; ok {
			// File originated from ctx.executable. return the
			// FilesToRunProvider from which it originated
			// instead. This is done so that if such a File is
			// provided to ctx.actions.run(tools=[...]), its
			// runfiles are added to the input root as well.
			decodedFilesToRun, err := model_starlark.DecodeStruct[TReference, TMetadata](
				thread,
				filesToRun,
			)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to decode FilesToRunProvider: %w", err)
			}
			return nil, decodedFilesToRun, nil
		}
		return typedValue, nil, nil
	case *model_starlark.Struct[TReference, TMetadata]:
		return nil, typedValue, nil
	default:
		panic("not a File or FilesToRunProvider")
	}
}

type ruleContextActions[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	ruleContext *ruleContext[TReference, TMetadata]
}

var _ eval.HasAttrs = (*ruleContextActions[object.GlobalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (ruleContextActions[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<ctx.actions>")
	return nil
}

func (ruleContextActions[TReference, TMetadata]) Type() string {
	return "ctx.actions"
}

func (ruleContextActions[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (rca *ruleContextActions[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	switch name {
	case "args":
		return eval.NewBuiltin("ctx.actions.args", rca.doArgs), nil
	case "declare_directory":
		return eval.NewBuiltin("ctx.actions.declare_directory", rca.doDeclareDirectory), nil
	case "declare_file":
		return eval.NewBuiltin("ctx.actions.declare_file", rca.doDeclareFile), nil
	case "declare_symlink":
		return eval.NewBuiltin("ctx.actions.declare_symlink", rca.doDeclareSymlink), nil
	case "expand_template":
		return eval.NewBuiltin("ctx.actions.expand_template", rca.doExpandTemplate), nil
	case "run":
		return eval.NewBuiltin("ctx.actions.run", rca.doRun), nil
	case "symlink":
		return eval.NewBuiltin("ctx.actions.symlink", rca.doSymlink), nil
	case "transform_info_file":
		return eval.NewBuiltin("ctx.actions.transform_info_file", rca.doTransformInfoFile), nil
	case "transform_version_file":
		return eval.NewBuiltin("ctx.actions.transform_version_file", rca.doTransformVersionFile), nil
	case "write":
		return eval.NewBuiltin("ctx.actions.write", rca.doWrite), nil
	default:
		return nil, eval.NoSuchAttrError{Value: rca, Name: name}
	}
}

var ruleContextActionsAttrNames = []string{
	"args",
	"declare_directory",
	"declare_file",
	"declare_symlink",
	"expand_template",
	"run",
	"symlink",
	"transform_info_file",
	"transform_version_file",
	"write",
}

func (ruleContextActions[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	return ruleContextActionsAttrNames, nil
}

func (rca *ruleContextActions[TReference, TMetadata]) doArgs(thread *eval.Thread, b *eval.Builtin, arguments eval.Args) (eval.Value, error) {
	return &args[TReference, TMetadata]{
		encoder:         rca.ruleContext.analysisEncoders.Encoder,
		paramFileFormat: model_analysis_pb.Args_Leaf_UseParamFile_SHELL,
	}, nil
}

func (rca *ruleContextActions[TReference, TMetadata]) doDeclareDirectory(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args.Positional))
	}
	var filename label.TargetName
	var sibling *model_starlark.File[TReference, TMetadata]
	rc := rca.ruleContext
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"filename", unpack.Bind(&filename, unpack.TargetName),
		"sibling?", unpack.Bind(&sibling, unpack.IfNotNone(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))),
	); err != nil {
		return nil, err
	}

	return rc.outputRegistrar.registerOutput(filename, sibling, model_starlark_pb.File_Owner_DIRECTORY)
}

func (rca *ruleContextActions[TReference, TMetadata]) doDeclareFile(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args.Positional))
	}
	var filename label.TargetName
	var sibling *model_starlark.File[TReference, TMetadata]
	rc := rca.ruleContext
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"filename", unpack.Bind(&filename, unpack.TargetName),
		"sibling?", unpack.Bind(&sibling, unpack.IfNotNone(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))),
	); err != nil {
		return nil, err
	}

	return rc.outputRegistrar.registerOutput(filename, sibling, model_starlark_pb.File_Owner_FILE)
}

func (rca *ruleContextActions[TReference, TMetadata]) doDeclareSymlink(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args.Positional))
	}
	var filename label.TargetName
	var sibling *model_starlark.File[TReference, TMetadata]
	rc := rca.ruleContext
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"filename", unpack.Bind(&filename, unpack.TargetName),
		"sibling?", unpack.Bind(&sibling, unpack.IfNotNone(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))),
	); err != nil {
		return nil, err
	}

	return rc.outputRegistrar.registerOutput(filename, sibling, model_starlark_pb.File_Owner_SYMLINK)
}

func (rca *ruleContextActions[TReference, TMetadata]) doExpandTemplate(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	if len(args.Positional) != 0 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want 0", b.Name(), len(args.Positional))
	}
	var output *targetOutput[TMetadata]
	var template *model_starlark.File[TReference, TMetadata]
	isExecutable := false
	var substitutions map[string][]byte
	rc := rca.ruleContext
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		// Required arguments.
		"output", unpack.Bind(&output, rc.outputRegistrar),
		"template", unpack.Bind(&template, unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
		// Optional arguments.
		// TODO: Add TemplateDict and computed_substitutions.
		"is_executable?", unpack.Bind(&isExecutable, unpack.Bool),
		"substitutions?", unpack.Bind(&substitutions, unpack.Dict(unpack.String, unpack.Bytes)),
	); err != nil {
		return nil, err
	}

	substitutionsList := make([]*model_analysis_pb.TargetOutputDefinition_ExpandTemplate_Substitution, 0, len(substitutions))
	for _, needle := range slices.Sorted(maps.Keys(substitutions)) {
		substitutionsList = append(substitutionsList, &model_analysis_pb.TargetOutputDefinition_ExpandTemplate_Substitution{
			Needle:      []byte(needle),
			Replacement: substitutions[needle],
		})
	}

	if output.fileType != model_starlark_pb.File_Owner_FILE {
		return nil, errors.New("output was not declared as a regular file")
	}
	patchedTemplate := model_core.Patch(rc.environment, template.GetDefinition())
	return eval.None, output.setDefinition(
		model_core.NewPatchedMessage(
			&model_analysis_pb.TargetOutputDefinition{
				Source: &model_analysis_pb.TargetOutputDefinition_ExpandTemplate_{
					ExpandTemplate: &model_analysis_pb.TargetOutputDefinition_ExpandTemplate{
						Template:      patchedTemplate.Message,
						IsExecutable:  isExecutable,
						Substitutions: substitutionsList,
					},
				},
			},
			patchedTemplate.Patcher,
		),
	)
}

// promoteStringArgumentsToArgs promotes a non-empty list of strings to
// an Args object that when evaluated expands to the same values.
func promoteStringArgumentsToArgs[TMetadata model_core.ReferenceMetadata](
	stringArgumentsListBuilder btree.Builder[*model_starlark_pb.List_Element, TMetadata],
	argsList btree.Builder[*model_analysis_pb.Args, TMetadata],
) error {
	stringArgumentsList, err := stringArgumentsListBuilder.FinalizeList()
	if err != nil {
		return err
	}
	return argsList.PushChild(
		model_core.NewPatchedMessage(
			&model_analysis_pb.Args{
				Level: &model_analysis_pb.Args_Leaf_{
					Leaf: &model_analysis_pb.Args_Leaf{
						Adds: []*model_analysis_pb.Args_Leaf_Add{{
							Level: &model_analysis_pb.Args_Leaf_Add_Leaf_{
								Leaf: &model_analysis_pb.Args_Leaf_Add_Leaf{
									Values: &model_starlark_pb.Value{
										Kind: &model_starlark_pb.Value_List{
											List: &model_starlark_pb.List{
												Elements: stringArgumentsList.Message,
											},
										},
									},
									FormatEach: "%s",
									Style: &model_analysis_pb.Args_Leaf_Add_Leaf_Separate_{
										Separate: &model_analysis_pb.Args_Leaf_Add_Leaf_Separate{},
									},
								},
							},
						}},
					},
				},
			},
			stringArgumentsList.Patcher,
		),
	)
}

// newFilesToRunProviderFromStruct converts a Starlark
// FilesToRunProvider object to a Protobuf message, so that it may be
// embedded in a target action definition.
func (rc *ruleContext[TReference, TMetadata]) newFilesToRunProviderFromStruct(thread *eval.Thread, s *model_starlark.Struct[TReference, TMetadata]) (model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata], *model_starlark.File[TReference, TMetadata], error) {
	// Unpack individual struct fields.
	type filesToRunProviderFields struct {
		executable           *model_starlark.File[TReference, TMetadata]
		runfilesFiles        *model_starlark.Depset[TReference, TMetadata]
		runfilesSymlinks     *model_starlark.Depset[TReference, TMetadata]
		runfilesRootSymlinks *model_starlark.Depset[TReference, TMetadata]
	}
	var fields filesToRunProviderFields
	if err := unpack.Attrs(
		func(thread *eval.Thread, fields *filesToRunProviderFields) []unpack.AttrUnpacker {
			return []unpack.AttrUnpacker{
				{Name: "executable", Unpacker: unpack.Bind(&fields.executable, unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))},
				{Name: "_runfiles_files", Unpacker: unpack.Bind(&fields.runfilesFiles, unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"))},
				{Name: "_runfiles_symlinks", Unpacker: unpack.Bind(&fields.runfilesSymlinks, unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"))},
				{Name: "_runfiles_root_symlinks", Unpacker: unpack.Bind(&fields.runfilesRootSymlinks, unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"))},
			}
		},
	).UnpackInto(thread, s, &fields); err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{}, nil, err
	}

	// Encode individual fields.
	valueEncodingOptions := rc.valueEncodingOptionsFactory.newValueEncodingOptions(rc.context, nil)
	executable := model_core.Patch(rc.environment, fields.executable.GetDefinition())
	runfilesFiles, err := fields.runfilesFiles.EncodeList(thread, nil, valueEncodingOptions)
	if err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{}, nil, fmt.Errorf("runfiles files: %w", err)
	}
	runfilesSymlinks, err := fields.runfilesSymlinks.EncodeList(thread, nil, valueEncodingOptions)
	if err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{}, nil, fmt.Errorf("runfiles symlinks: %w", err)
	}
	runfilesRootSymlinks, err := fields.runfilesRootSymlinks.EncodeList(thread, nil, valueEncodingOptions)
	if err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{}, nil, fmt.Errorf("runfiles root symlinks: %w", err)
	}

	patchedFilesToRunProvider, err := inlinedtree.Build(
		inlinedtree.CandidateList[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{
			// Fields that should always be inlined into the
			// FilesToRunProvider.
			inlinedtree.AlwaysInline(
				executable.Patcher,
				func(filesToRunProvider model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]) {
					filesToRunProvider.Message.Executable = executable.Message
				},
			),
			// Fields that can be stored externally if needed.
			{
				ExternalMessage: model_core.ProtoListToBinaryMarshaler(runfilesFiles),
				Encoder:         rc.analysisEncoders.Encoder,
				ParentAppender: func(
					filesToRunProvider model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					runfilesFiles, err := btree.MaybeMergeNodes(
						runfilesFiles.Message,
						externalObject,
						filesToRunProvider.Patcher,
						btree.Capturing(
							rc.context,
							valueEncodingOptions.ObjectCapturer,
							model_starlark.ComputeListParentNode,
						),
					)
					if err != nil {
						return err
					}
					filesToRunProvider.Message.RunfilesFiles = runfilesFiles
					return nil
				},
			},
			{
				ExternalMessage: model_core.ProtoListToBinaryMarshaler(runfilesSymlinks),
				Encoder:         rc.analysisEncoders.Encoder,
				ParentAppender: func(
					filesToRunProvider model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					runfilesSymlinks, err := btree.MaybeMergeNodes(
						runfilesSymlinks.Message,
						externalObject,
						filesToRunProvider.Patcher,
						btree.Capturing(
							rc.context,
							valueEncodingOptions.ObjectCapturer,
							model_starlark.ComputeListParentNode,
						),
					)
					if err != nil {
						return err
					}
					filesToRunProvider.Message.RunfilesSymlinks = runfilesSymlinks
					return nil
				},
			},
			{
				ExternalMessage: model_core.ProtoListToBinaryMarshaler(runfilesRootSymlinks),
				Encoder:         rc.analysisEncoders.Encoder,
				ParentAppender: func(
					filesToRunProvider model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					runfilesRootSymlinks, err := btree.MaybeMergeNodes(
						runfilesRootSymlinks.Message,
						externalObject,
						filesToRunProvider.Patcher,
						btree.Capturing(
							rc.context,
							valueEncodingOptions.ObjectCapturer,
							model_starlark.ComputeListParentNode,
						),
					)
					if err != nil {
						return err
					}
					filesToRunProvider.Message.RunfilesRootSymlinks = runfilesRootSymlinks
					return nil
				},
			},
		},
		rc.computer.chunkingParameters.GetInlinedTreeOptions(),
	)
	return patchedFilesToRunProvider, fields.executable, err
}

func (rca *ruleContextActions[TReference, TMetadata]) doRun(thread *eval.Thread, b *eval.Builtin, fnArgs eval.Args) (eval.Value, error) {
	if len(fnArgs.Positional) != 0 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want 0", b.Name(), len(fnArgs.Positional))
	}
	currentFilePackage, err := model_starlark.CurrentFilePackage(thread)
	if err != nil {
		return nil, err
	}
	var executable any
	var outputs []*targetOutput[TMetadata]
	var arguments []any
	var env map[string]string
	execGroup := ""
	var executionRequirements map[string]string
	var inputs *model_starlark.Depset[TReference, TMetadata]
	mnemonic := ""
	var progressMessage string
	var resourceSet eval.Function
	var toolchain *label.ResolvedLabel
	var tools []any
	useDefaultShellEnv := false
	rc := rca.ruleContext
	if err := eval.UnpackArgs(
		thread, b.Name(), fnArgs,
		// Required arguments.
		"executable", unpack.Bind(&executable, unpack.Or([]unpack.UnpackerInto[any]{
			unpack.Decay(unpack.String),
			unpack.Decay(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
			unpack.Decay(unpack.Type[*model_starlark.Struct[TReference, TMetadata]]("struct")),
		})),
		"outputs", unpack.Bind(&outputs, unpack.List(rc.outputRegistrar)),
		// Optional arguments.
		"arguments?", unpack.Bind(&arguments, unpack.List(unpack.Or([]unpack.UnpackerInto[any]{
			unpack.Decay(unpack.Type[*args[TReference, TMetadata]]("Args")),
			unpack.Decay(unpack.String),
		}))),
		"env?", unpack.Bind(&env, unpack.Dict(unpack.String, unpack.String)),
		"exec_group?", unpack.Bind(&execGroup, unpack.IfNotNone(unpack.String)),
		"execution_requirements?", unpack.Bind(&executionRequirements, unpack.IfNotNone(unpack.Dict(unpack.String, unpack.String))),
		"inputs?", unpack.Bind(&inputs, unpack.Or([]unpack.UnpackerInto[*model_starlark.Depset[TReference, TMetadata]]{
			unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"),
			model_starlark.NewListToDepsetUnpackerInto[TReference, TMetadata](
				unpack.Canonicalize(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
			),
		})),
		"mnemonic?", unpack.Bind(&mnemonic, unpack.IfNotNone(unpack.String)),
		"progress_message?", unpack.Bind(&progressMessage, unpack.IfNotNone(unpack.String)),
		"resource_set?", unpack.Bind(&resourceSet, unpack.IfNotNone(unpack.Type[eval.Function]("function"))),
		"toolchain?", unpack.Bind(&toolchain, unpack.IfNotNone(unpack.Pointer(model_starlark.NewLabelOrStringUnpackerInto[TReference, TMetadata](currentFilePackage)))),
		"tools?", unpack.Bind(&tools, unpack.Or([]unpack.UnpackerInto[[]any]{
			unpack.Singleton(unpack.Decay(unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"))),
			unpack.List(unpack.Or([]unpack.UnpackerInto[any]{
				unpack.Decay(unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset")),
				unpack.Decay(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
				unpack.Decay(unpack.Type[*model_starlark.Struct[TReference, TMetadata]]("struct")),
			})),
		})),
		"use_default_shell_env?", unpack.Bind(&useDefaultShellEnv, unpack.Bool),
	); err != nil {
		return nil, err
	}

	valueEncodingOptions := rc.valueEncodingOptionsFactory.newValueEncodingOptions(rc.context, nil)
	var inputsDirect []eval.Value
	toolsParentNodeComputer := btree.Capturing(rc.context, valueEncodingOptions.ObjectCapturer, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.FilesToRunProvider, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider, TMetadata] {
		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.FilesToRunProvider {
			return &model_analysis_pb.FilesToRunProvider{
				Level: &model_analysis_pb.FilesToRunProvider_Parent_{
					Parent: &model_analysis_pb.FilesToRunProvider_Parent{
						Reference: patcher.AddDecodableReference(createdObject),
					},
				},
			}
		})
	})
	toolsBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			valueEncodingOptions.ObjectChunkingParameters,
			/* isParent = */ func(filesToRunProvider *model_analysis_pb.FilesToRunProvider) bool {
				return filesToRunProvider.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			rc.analysisEncoders.Encoder,
			valueEncodingOptions.ObjectReferenceFormat,
			toolsParentNodeComputer,
		),
	)
	defer toolsBuilder.Discard()

	// Derive argv0 from the executable. Even though it's not
	// explicitly documented, the executable is also treated as a
	// tool dependency.
	argv0, ok := executable.(string)
	if !ok {
		executableFile, executableFilesToRunProvider, err := rc.getFileOrFilesToRunProvider(thread, executable)
		if err != nil {
			return nil, fmt.Errorf("executable: %w", err)
		}

		if executableFile != nil {
			inputsDirect = append(inputsDirect, executableFile)
		} else {
			var patchedFilesToRun model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]
			patchedFilesToRun, executableFile, err = rc.newFilesToRunProviderFromStruct(thread, executableFilesToRunProvider)
			if err != nil {
				return nil, fmt.Errorf("executable: %w", err)
			}
			if err := toolsBuilder.PushChild(
				model_core.NewPatchedMessage(
					&model_analysis_pb.FilesToRunProvider{
						Level: &model_analysis_pb.FilesToRunProvider_Leaf_{
							Leaf: patchedFilesToRun.Message,
						},
					},
					patchedFilesToRun.Patcher,
				),
			); err != nil {
				return nil, fmt.Errorf("executable: %w", err)
			}
		}

		argv0, err = model_starlark.FileGetInputRootPath(executableFile.GetDefinition(), nil)
		if err != nil {
			return nil, fmt.Errorf("executable: %w", err)
		}
	}

	// Use the name of the first output file as a somewhat stable
	// identifier of the action. As this identifier is local to the
	// configured target, it doesn't need to be too long to be
	// unique.
	if len(outputs) == 0 {
		return nil, errors.New("action has no outputs")
	}
	actionID := []byte(outputs[0].packageRelativePath.String())
	if maxLength := 16; len(actionID) >= maxLength {
		h := sha256.Sum256(actionID)
		actionID = h[:maxLength]
	}

	// Encode all arguments. Arguments may be a mixture of strings
	// and Args objects.
	//
	// Promote any runs of string arguments to equivalent Args
	// objects taking a list. That way, computation of target
	// actions only needs to process Args objects.
	stringArgumentsListBuilder := model_starlark.NewListBuilder(valueEncodingOptions)
	encodedArgv0, err := model_starlark.EncodeValue[TReference, TMetadata](thread, eval.String(argv0), nil, nil, valueEncodingOptions)
	if err != nil {
		return nil, err
	}
	if err := stringArgumentsListBuilder.PushChild(
		model_core.NewPatchedMessage(
			&model_starlark_pb.List_Element{
				Level: &model_starlark_pb.List_Element_Leaf{
					Leaf: encodedArgv0.Message,
				},
			},
			encodedArgv0.Patcher,
		),
	); err != nil {
		return nil, err
	}

	argsParentNodeComputer := btree.Capturing(rc.context, valueEncodingOptions.ObjectCapturer, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.Args, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.Args, TMetadata] {
		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.Args {
			return &model_analysis_pb.Args{
				Level: &model_analysis_pb.Args_Parent_{
					Parent: &model_analysis_pb.Args_Parent{
						Reference: patcher.AddDecodableReference(createdObject),
					},
				},
			}
		})
	})
	argsListBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			valueEncodingOptions.ObjectChunkingParameters,
			/* isParent = */ func(args *model_analysis_pb.Args) bool {
				return args.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			rc.analysisEncoders.Encoder,
			valueEncodingOptions.ObjectReferenceFormat,
			argsParentNodeComputer,
		),
	)
	defer argsListBuilder.Discard()

	gotStringArguments := true
	for _, argument := range arguments {
		switch typedArgument := argument.(type) {
		case *args[TReference, TMetadata]:
			if gotStringArguments {
				if err := promoteStringArgumentsToArgs(stringArgumentsListBuilder, argsListBuilder); err != nil {
					return nil, err
				}
				gotStringArguments = false
			}
			encodedArgs, err := typedArgument.Encode(thread, nil, valueEncodingOptions)
			if err != nil {
				return nil, err
			}
			if err := argsListBuilder.PushChild(
				model_core.NewPatchedMessage(
					&model_analysis_pb.Args{
						Level: &model_analysis_pb.Args_Leaf_{
							Leaf: encodedArgs.Message,
						},
					},
					encodedArgs.Patcher,
				),
			); err != nil {
				return nil, err
			}
		case string:
			if !gotStringArguments {
				stringArgumentsListBuilder = model_starlark.NewListBuilder(valueEncodingOptions)
				gotStringArguments = true
			}
			encodedTypedArgument, err := model_starlark.EncodeValue[TReference, TMetadata](thread, eval.String(typedArgument), nil, nil, valueEncodingOptions)
			if err != nil {
				return nil, err
			}
			if err := stringArgumentsListBuilder.PushChild(
				model_core.NewPatchedMessage(
					&model_starlark_pb.List_Element{
						Level: &model_starlark_pb.List_Element_Leaf{
							Leaf: encodedTypedArgument.Message,
						},
					},
					encodedTypedArgument.Patcher,
				),
			); err != nil {
				return nil, err
			}
		default:
			panic("unexpected argument type")
		}
	}
	if gotStringArguments {
		if err := promoteStringArgumentsToArgs(stringArgumentsListBuilder, argsListBuilder); err != nil {
			return nil, err
		}
	}
	argsList, err := argsListBuilder.FinalizeList()
	if err != nil {
		return nil, err
	}

	envList, envParentNodeComputer, err := convertDictToEnvironmentVariableList(
		rc.context,
		env,
		rc.actionEncoder,
		valueEncodingOptions.ObjectReferenceFormat,
		rc.environment,
	)
	if err != nil {
		return nil, err
	}

	execGroups := rc.ruleDefinition.Message.ExecGroups
	execGroupIndex, ok := sort.Find(
		len(execGroups),
		func(i int) int { return strings.Compare(execGroup, execGroups[i].Name) },
	)
	if !ok {
		return nil, fmt.Errorf("rule does not have an exec group with name %#v", execGroup)
	}

	// Determine the set of output paths to capture. Those need to
	// be stored in a tree, which the worker uses after execution
	// completes to determine which parts of the input root to
	// capture.
	var outputPathPatternSet model_command.PathPatternSet[TMetadata]
	for _, output := range outputs {
		if err := output.setDefinition(
			model_core.NewSimplePatchedMessage[TMetadata](
				&model_analysis_pb.TargetOutputDefinition{
					Source: &model_analysis_pb.TargetOutputDefinition_ActionId{
						ActionId: actionID,
					},
				},
			),
		); err != nil {
			return nil, err
		}
		outputPathPatternSet.Add(strings.SplitSeq(output.packageRelativePath.String(), "/"))
	}
	outputPathPatternChildren, err := outputPathPatternSet.ToProto(
		rc.context,
		rc.actionEncoder,
		rc.computer.chunkingParameters.GetInlinedTreeOptions(),
		rc.environment,
	)
	if err != nil {
		return nil, err
	}

	// Tools such as compilers tend to expect that parent
	// directories of output files already exist. Create a directory
	// hierachy containing the parent directories of all output
	// files. This directory will get merged into the input root.
	var initialOutputDirectory changeTrackingDirectory[TReference, TMetadata]
	for _, output := range outputs {
		stack := util.NewNonEmptyStack(&initialOutputDirectory)
		var r path.ScopeWalker
		if output.fileType == model_starlark_pb.File_Owner_DIRECTORY {
			// For directory outputs, Bazel also creates the
			// directory itself. Not just its parents.
			r = &changeTrackingDirectoryNewDirectoryResolver[TReference, TMetadata]{stack: stack}
		} else {
			r = &changeTrackingDirectoryNewFileResolver[TReference, TMetadata]{stack: stack}
		}
		if err := path.Resolve(path.UNIXFormat.NewParser(output.packageRelativePath.String()), r); err != nil {
			return nil, fmt.Errorf("failed to create parent directory for output %#v: %w", output.packageRelativePath.String(), err)
		}
	}
	var createdInitialOutputDirectory model_filesystem.CreatedDirectory[TMetadata]
	group, groupCtx := errgroup.WithContext(rc.context)
	group.Go(func() error {
		return model_filesystem.CreateDirectoryMerkleTree(
			groupCtx,
			semaphore.NewWeighted(1),
			group,
			rc.directoryCreationParameters,
			&capturableChangeTrackingDirectory[TReference, TMetadata]{
				directory: &initialOutputDirectory,
				options:   &capturableChangeTrackingDirectoryOptions[TReference, TMetadata]{},
			},
			model_filesystem.NewSimpleDirectoryMerkleTreeCapturer(rc.environment),
			&createdInitialOutputDirectory,
		)
	})
	if err := group.Wait(); err != nil {
		return nil, err
	}

	// Gather inputs and tools.
	//
	// If tools are provided in the form of depsets, or Files that
	// are not provided by a label attribute marked executable=True,
	// they will not have their runfiles directories added to the
	// input root. Demote such tools to regular inputs.
	var inputsTransitive []*model_starlark.Depset[TReference, TMetadata]
	if inputs != nil {
		inputsTransitive = append(inputsTransitive, inputs)
	}
	for i, tool := range tools {
		if d, ok := tool.(*model_starlark.Depset[TReference, TMetadata]); ok {
			inputsTransitive = append(inputsTransitive, d)
		} else {
			if toolFile, toolFilesToRunProvider, err := rc.getFileOrFilesToRunProvider(thread, tool); err != nil {
				return nil, fmt.Errorf("tool at index %d: %w", i, err)
			} else if toolFile != nil {
				inputsDirect = append(inputsDirect, toolFile)
			} else {
				patchedFilesToRun, _, err := rc.newFilesToRunProviderFromStruct(thread, toolFilesToRunProvider)
				if err != nil {
					return nil, fmt.Errorf("tool at index %d: %w", i, err)
				}
				if err := toolsBuilder.PushChild(
					model_core.NewPatchedMessage(
						&model_analysis_pb.FilesToRunProvider{
							Level: &model_analysis_pb.FilesToRunProvider_Leaf_{
								Leaf: patchedFilesToRun.Message,
							},
						},
						patchedFilesToRun.Patcher,
					),
				); err != nil {
					return nil, fmt.Errorf("tool at index %d: %w", i, err)
				}
			}
		}
	}

	mergedInputs, err := model_starlark.NewDepsetContents(thread, inputsDirect, inputsTransitive, model_starlark_pb.Depset_DEFAULT)
	if err != nil {
		return nil, err
	}
	encodedInputs, err := mergedInputs.EncodeList(thread, nil, valueEncodingOptions)
	if err != nil {
		return nil, err
	}

	toolsList, err := toolsBuilder.FinalizeList()
	if err != nil {
		return nil, err
	}

	actionDefinition, err := inlinedtree.Build(
		inlinedtree.CandidateList[*model_analysis_pb.TargetActionDefinition, TMetadata]{
			// Fields that should always be inlined into the
			// action definition.
			inlinedtree.AlwaysInline(
				model_core.NewReferenceMessagePatcher[TMetadata](),
				func(actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata]) {
					actionDefinition.Message.PlatformPkixPublicKey = rc.execGroups[execGroupIndex].platformPkixPublicKey
					actionDefinition.Message.UseDefaultShellEnv = useDefaultShellEnv
				},
			),
			// Fields that can be stored externally if needed.
			{
				ExternalMessage: model_core.ProtoListToBinaryMarshaler(argsList),
				Encoder:         rc.analysisEncoders.Encoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					arguments, err := btree.MaybeMergeNodes(
						argsList.Message,
						externalObject,
						actionDefinition.Patcher,
						argsParentNodeComputer,
					)
					if err != nil {
						return err
					}
					actionDefinition.Message.Arguments = arguments
					return nil
				},
			},
			{
				ExternalMessage: model_core.ProtoListToBinaryMarshaler(envList),
				Encoder:         rc.actionEncoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					env, err := btree.MaybeMergeNodes(
						envList.Message,
						externalObject,
						actionDefinition.Patcher,
						envParentNodeComputer,
					)
					if err != nil {
						return err
					}
					actionDefinition.Message.Env = env
					return nil
				},
			},
			{
				ExternalMessage: model_core.ProtoListToBinaryMarshaler(encodedInputs),
				Encoder:         rc.analysisEncoders.Encoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					inputs, err := btree.MaybeMergeNodes(
						encodedInputs.Message,
						externalObject,
						actionDefinition.Patcher,
						btree.Capturing(
							rc.context,
							valueEncodingOptions.ObjectCapturer,
							model_starlark.ComputeListParentNode,
						),
					)
					if err != nil {
						return err
					}
					actionDefinition.Message.Inputs = inputs
					return nil
				},
			},
			{
				ExternalMessage: model_core.ProtoListToBinaryMarshaler(toolsList),
				Encoder:         rc.analysisEncoders.Encoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					tools, err := btree.MaybeMergeNodes(
						toolsList.Message,
						externalObject,
						actionDefinition.Patcher,
						toolsParentNodeComputer,
					)
					if err != nil {
						return err
					}
					actionDefinition.Message.Tools = tools
					return nil
				},
			},
			{
				ExternalMessage: model_core.ProtoToBinaryMarshaler(outputPathPatternChildren),
				Encoder:         rc.actionEncoder,
				ParentAppender: inlinedtree.Capturing(rc.context, rc.environment, func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.MetadataEntry[TMetadata]],
				) {
					actionDefinition.Message.OutputPathPattern = model_command.GetPathPatternWithChildren(
						outputPathPatternChildren,
						externalObject,
						actionDefinition.Patcher,
					)
				}),
			},
			{
				ExternalMessage: model_core.ProtoToBinaryMarshaler(createdInitialOutputDirectory.Message),
				Encoder:         rc.actionEncoder,
				ParentAppender: inlinedtree.Capturing(rc.context, rc.environment, func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.MetadataEntry[TMetadata]],
				) {
					actionDefinition.Message.InitialOutputDirectory = model_filesystem.GetDirectoryWithContents(
						&createdInitialOutputDirectory,
						externalObject,
						actionDefinition.Patcher,
					)
				}),
			},
		},
		rc.computer.chunkingParameters.GetInlinedTreeOptions(),
	)
	if err != nil {
		return nil, err
	}
	rc.actions = append(rc.actions, model_core.NewPatchedMessage(
		&model_analysis_pb.ConfiguredTarget_Value_Action_Leaf{
			Id:         actionID,
			Definition: actionDefinition.Message,
		},
		actionDefinition.Patcher,
	))
	return eval.None, nil
}

type singleSymlinkDirectory[TFile, TDirectory model_core.ReferenceMetadata] struct {
	components []path.Component
	target     path.Parser
}

func (singleSymlinkDirectory[TFile, TDirectory]) Close() error {
	return nil
}

func (d *singleSymlinkDirectory[TFile, TDirectory]) ReadDir() ([]filesystem.FileInfo, error) {
	fileType := filesystem.FileTypeDirectory
	if len(d.components) == 1 {
		fileType = filesystem.FileTypeSymlink
	}
	return []filesystem.FileInfo{
		filesystem.NewFileInfo(d.components[0], fileType, false),
	}, nil
}

func (d *singleSymlinkDirectory[TFile, TDirectory]) Readlink(name path.Component) (path.Parser, error) {
	return d.target, nil
}

func (d *singleSymlinkDirectory[TFile, TDirectory]) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[TDirectory], model_filesystem.CapturableDirectory[TDirectory, TFile], error) {
	return nil, &singleSymlinkDirectory[TFile, TDirectory]{
		components: d.components[1:],
		target:     d.target,
	}, nil
}

func (singleSymlinkDirectory[TFile, TDirectory]) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[TFile], error) {
	panic("directory only contains a symlink")
}

func (rca *ruleContextActions[TReference, TMetadata]) doSymlink(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	var output *targetOutput[TMetadata]
	var targetFile *model_starlark.File[TReference, TMetadata]
	var targetPath path.Parser
	isExecutable := false
	progressMessage := ""
	useExecRootForSource := false
	rc := rca.ruleContext
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"output", unpack.Bind(&output, rc.outputRegistrar),
		"target_file?", unpack.Bind(&targetFile, unpack.IfNotNone(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))),
		"target_path?", unpack.Bind(&targetPath, unpack.PathParser(path.UNIXFormat)),
		"is_executable?", unpack.Bind(&isExecutable, unpack.Bool),
		"progress_message?", unpack.Bind(&progressMessage, unpack.IfNotNone(unpack.String)),
		"use_exec_root_for_source?", unpack.Bind(&useExecRootForSource, unpack.Bool),
	); err != nil {
		return nil, err
	}

	if useExecRootForSource {
		return nil, errors.New("this implementation does not support use_exec_root_for_source=True")
	}
	if isExecutable && output.fileType != model_starlark_pb.File_Owner_FILE {
		return nil, errors.New("is_executable=True can only be used in combination with regular file outputs")
	}

	if targetFile != nil {
		if targetPath != nil {
			return nil, errors.New("target_file and target_path cannot be specified at the same time")
		}
		if output.fileType != model_starlark_pb.File_Owner_DIRECTORY && output.fileType != model_starlark_pb.File_Owner_FILE {
			return nil, errors.New("target_file can only be used in combination with outputs that are declared as directories or regular files")
		}
		targetFileDefinition := targetFile.GetDefinition()
		if o := targetFileDefinition.Message.Owner; o != nil && output.fileType != o.Type {
			return nil, errors.New("output and target_file have different file types")
		}

		patchedTargetFileDefinition := model_core.Patch(rc.environment, targetFileDefinition)
		return eval.None, output.setDefinition(
			model_core.NewPatchedMessage(
				&model_analysis_pb.TargetOutputDefinition{
					Source: &model_analysis_pb.TargetOutputDefinition_Symlink_{
						Symlink: &model_analysis_pb.TargetOutputDefinition_Symlink{
							Target:       patchedTargetFileDefinition.Message,
							IsExecutable: isExecutable,
						},
					},
				},
				patchedTargetFileDefinition.Patcher,
			),
		)
	}

	if targetPath == nil {
		return nil, errors.New("one of target_file or target_path needs to be specified")
	}
	if output.fileType != model_starlark_pb.File_Owner_SYMLINK {
		return nil, errors.New("target_path can only be used in combination with outputs that are declared as symbolic links")
	}

	return eval.None, rc.setOutputToStaticDirectory(
		output,
		&singleSymlinkDirectory[TMetadata, TMetadata]{
			components: output.packageRelativePath.ToComponents(),
			target:     targetPath,
		},
	)
}

func (ruleContextActions[TReference, TMetadata]) doTransformInfoFile(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	return eval.None, nil
}

func (ruleContextActions[TReference, TMetadata]) doTransformVersionFile(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	return eval.None, nil
}

type singleFileDirectory[TFile, TDirectory model_core.ReferenceMetadata] struct {
	components   []path.Component
	isExecutable bool
	file         model_filesystem.CapturableFile[TFile]
}

func (singleFileDirectory[TFile, TDirectory]) Close() error {
	return nil
}

func (d *singleFileDirectory[TFile, TDirectory]) ReadDir() ([]filesystem.FileInfo, error) {
	fileType := filesystem.FileTypeDirectory
	isExecutable := false
	if len(d.components) == 1 {
		fileType = filesystem.FileTypeRegularFile
		isExecutable = d.isExecutable
	}
	return []filesystem.FileInfo{
		filesystem.NewFileInfo(d.components[0], fileType, isExecutable),
	}, nil
}

func (singleFileDirectory[TFile, TDirectory]) Readlink(name path.Component) (path.Parser, error) {
	panic("directory only contains a regular file")
}

func (d *singleFileDirectory[TFile, TDirectory]) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[TDirectory], model_filesystem.CapturableDirectory[TDirectory, TFile], error) {
	return nil, &singleFileDirectory[TFile, TDirectory]{
		components:   d.components[1:],
		isExecutable: d.isExecutable,
		file:         d.file,
	}, nil
}

func (d *singleFileDirectory[TFile, TDirectory]) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[TFile], error) {
	return d.file, nil
}

func (rca *ruleContextActions[TReference, TMetadata]) doWrite(thread *eval.Thread, b *eval.Builtin, fnArgs eval.Args) (eval.Value, error) {
	if len(fnArgs.Positional) > 3 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 3", b.Name(), len(fnArgs.Positional))
	}
	var output *targetOutput[TMetadata]
	var content any
	isExecutable := false
	mnemonic := ""
	rc := rca.ruleContext
	if err := eval.UnpackArgs(
		thread, b.Name(), fnArgs,
		"output", unpack.Bind(&output, rc.outputRegistrar),
		"content", unpack.Bind(&content, unpack.Or([]unpack.UnpackerInto[any]{
			unpack.Decay(unpack.Type[*args[TReference, TMetadata]]("Args")),
			unpack.Decay(model_starlark.NewStoredStringFileContentsUnpackerInto[TReference, TMetadata]()),
			unpack.Decay(unpack.Bytes),
		})),
		"is_executable?", unpack.Bind(&isExecutable, unpack.Bool),
		"mnemonic?", unpack.Bind(&mnemonic, unpack.IfNotNone(unpack.String)),
	); err != nil {
		return nil, err
	}

	if output.fileType != model_starlark_pb.File_Owner_FILE {
		return nil, errors.New("output was not declared as a regular file")
	}

	switch typedContent := content.(type) {
	case *args[TReference, TMetadata]:
		valueEncodingOptions := rc.valueEncodingOptionsFactory.newValueEncodingOptions(rc.context, nil)
		encodedContent, err := typedContent.Encode(thread, nil, valueEncodingOptions)
		if err != nil {
			return nil, err
		}
		return eval.None, output.setDefinition(
			model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetOutputDefinition {
				return &model_analysis_pb.TargetOutputDefinition{
					Source: &model_analysis_pb.TargetOutputDefinition_Write_{
						Write: &model_analysis_pb.TargetOutputDefinition_Write{
							Content:      encodedContent.Merge(patcher),
							IsExecutable: isExecutable,
						},
					},
				}
			}),
		)
	case model_core.Message[*model_filesystem_pb.FileContents, TReference]:
		// Large string that is backed by storage. Its Merkle
		// tree is identical to the one that would be created
		// below, so there is no need to read the string.
		return eval.None, rc.setOutputToStaticDirectory(
			output,
			&singleFileDirectory[TMetadata, TMetadata]{
				components:   output.packageRelativePath.ToComponents(),
				isExecutable: isExecutable,
				file:         model_filesystem.NewSimpleCapturableFile(model_core.Patch(rc.environment, typedContent)),
			},
		)
	case []byte:
		fileContents, err := model_filesystem.CreateFileMerkleTree(
			rc.context,
			rc.fileCreationParameters,
			bytes.NewReader(typedContent),
			model_filesystem.NewSimpleFileMerkleTreeCapturer(rc.environment),
		)
		if err != nil {
			return nil, err
		}

		return eval.None, rc.setOutputToStaticDirectory(
			output,
			&singleFileDirectory[TMetadata, TMetadata]{
				components:   output.packageRelativePath.ToComponents(),
				isExecutable: isExecutable,
				file:         model_filesystem.NewSimpleCapturableFile(fileContents),
			},
		)
	default:
		panic("unexpected argument type")
	}
}

type ruleContextExecGroups[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	ruleContext *ruleContext[TReference, TMetadata]
}

var _ eval.Mapping = (*ruleContextExecGroups[object.GlobalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (ruleContextExecGroups[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<ctx.exec_groups>")
	return nil
}

func (ruleContextExecGroups[TReference, TMetadata]) Type() string {
	return "ctx.exec_groups"
}

func (ruleContextExecGroups[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (rca *ruleContextExecGroups[TReference, TMetadata]) Get(thread *eval.Thread, key eval.Value) (eval.Value, bool, error) {
	var execGroupName string
	if err := unpack.String.UnpackInto(thread, key, &execGroupName); err != nil {
		return nil, false, err
	}

	rc := rca.ruleContext
	execGroups := rc.ruleDefinition.Message.ExecGroups
	execGroupIndex, ok := sort.Find(
		len(execGroups),
		func(i int) int { return strings.Compare(execGroupName, execGroups[i].Name) },
	)
	if !ok {
		return nil, false, nil
	}
	return model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
		"toolchains": &toolchainContext[TReference, TMetadata]{
			ruleContext:    rc,
			execGroupIndex: execGroupIndex,
		},
	}), true, nil
}

type toolchainContext[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	ruleContext    *ruleContext[TReference, TMetadata]
	execGroupIndex int
}

var _ eval.Mapping = (*toolchainContext[object.GlobalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (toolchainContext[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<toolchain context>")
	return nil
}

func (toolchainContext[TReference, TMetadata]) Type() string {
	return "ToolchainContext"
}

func (toolchainContext[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (tc *toolchainContext[TReference, TMetadata]) Get(thread *eval.Thread, v eval.Value) (eval.Value, bool, error) {
	rc := tc.ruleContext
	currentFilePackage, err := model_starlark.CurrentFilePackage(thread)
	if err != nil {
		return nil, false, err
	}
	labelUnpackerInto := unpack.Stringer(model_starlark.NewLabelOrStringUnpackerInto[TReference, TMetadata](currentFilePackage))
	var toolchainType string
	if err := labelUnpackerInto.UnpackInto(thread, v, &toolchainType); err != nil {
		return nil, false, err
	}

	namedExecGroup := rc.ruleDefinition.Message.ExecGroups[tc.execGroupIndex]
	execGroupDefinition := namedExecGroup.ExecGroup
	if execGroupDefinition == nil {
		return nil, false, errors.New("rule definition lacks exec group definition")
	}

	toolchains := execGroupDefinition.Toolchains
	toolchainIndex, ok := sort.Find(
		len(toolchains),
		func(i int) int { return strings.Compare(toolchainType, toolchains[i].ToolchainType) },
	)
	if !ok {
		// Exec group does not depend on the toolchain type.
		return nil, false, nil
	}

	execGroup := &rc.execGroups[tc.execGroupIndex]
	toolchainInfo := execGroup.toolchainInfos[toolchainIndex]
	if toolchainInfo == nil {
		toolchainIdentifier := execGroup.toolchainIdentifiers[toolchainIndex]
		if toolchainIdentifier == "" {
			// Toolchain was optional, and no matching
			// toolchain was found.
			toolchainInfo = eval.None
		} else {
			encodedToolchainInfo, err := getProviderFromConfiguredTarget(
				rc.environment,
				toolchainIdentifier,
				model_core.Patch(
					rc.environment,
					rc.configurationReference,
				),
				toolchainInfoProviderIdentifier,
			)
			if err != nil {
				return nil, true, err
			}

			toolchainInfo, err = model_starlark.DecodeStruct[TReference, TMetadata](
				thread,
				model_core.Nested(encodedToolchainInfo, &model_starlark_pb.Struct{
					ProviderInstanceProperties: &model_starlark_pb.Provider_InstanceProperties{
						ProviderIdentifier: toolchainInfoProviderIdentifier.String(),
					},
					Fields: encodedToolchainInfo.Message,
				}),
			)
			if err != nil {
				return nil, true, err
			}
		}
		execGroup.toolchainInfos[toolchainIndex] = toolchainInfo
	}
	return toolchainInfo, true, nil
}

type ruleContextExecGroupState struct {
	platformPkixPublicKey []byte
	toolchainIdentifiers  []string
	toolchainInfos        []eval.Value
}

// argsAdd records all arguments provided to Args.add(), Args.add_all()
// and Args.add_joined().
type argsAdd[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	startWith         *wrapperspb.StringValue
	values            eval.Value
	expandDirectories bool
	mapEach           eval.Function
	formatEach        string
	omitIfEmpty       bool
	uniquify          bool
	setStyle          func(leaf *model_analysis_pb.Args_Leaf_Add_Leaf)
}

// argsUseParamFile records all arguments provided to
// Args.use_param_file().
type argsUseParamFile struct {
	paramFileArg string
	useAlways    bool
}

// args records the state of an Args object created through
// ctx.actions.args().
type args[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	encoder         model_encoding.DeterministicBinaryEncoder
	adds            []argsAdd[TReference, TMetadata]
	paramFileFormat model_analysis_pb.Args_Leaf_UseParamFile_Format
	useParamFile    *argsUseParamFile
}

var _ eval.HasAttrs = (*args[object.LocalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (args[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<Args>")
	return nil
}

func (args[TReference, TMetadata]) Type() string {
	return "Args"
}

func (args[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (a *args[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	switch name {
	case "add":
		return eval.NewBuiltin("Args.add", a.doAdd), nil
	case "add_all":
		return eval.NewBuiltin("Args.add_all", a.doAddAll), nil
	case "add_joined":
		return eval.NewBuiltin("Args.add_joined", a.doAddJoined), nil
	case "set_param_file_format":
		return eval.NewBuiltin("Args.set_param_file_format", a.doSetParamFileFormat), nil
	case "use_param_file":
		return eval.NewBuiltin("Args.use_param_file", a.doUseParamFile), nil
	default:
		return nil, eval.NoSuchAttrError{Value: a, Name: name}
	}
}

var argsAttrNames = []string{
	"add",
	"add_all",
	"add_joined",
	"set_param_file_format",
	"use_param_file",
}

func (args[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	return argsAttrNames, nil
}

func (a *args[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *model_starlark.ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_analysis_pb.Args_Leaf, TMetadata], error) {
	var useParamFile *model_analysis_pb.Args_Leaf_UseParamFile
	if u := a.useParamFile; u != nil {
		useParamFile = &model_analysis_pb.Args_Leaf_UseParamFile{
			Format:       a.paramFileFormat,
			ParamFileArg: u.paramFileArg,
			UseAlways:    u.useAlways,
		}
	}

	addsListBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			options.ObjectChunkingParameters,
			/* isParent = */ func(add *model_analysis_pb.Args_Leaf_Add) bool {
				return add.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			a.encoder,
			options.ObjectReferenceFormat,
			btree.Capturing(options.Context, options.ObjectCapturer, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.Args_Leaf_Add, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.Args_Leaf_Add, TMetadata] {
				return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.Args_Leaf_Add {
					return &model_analysis_pb.Args_Leaf_Add{
						Level: &model_analysis_pb.Args_Leaf_Add_Parent_{
							Parent: &model_analysis_pb.Args_Leaf_Add_Parent{
								Reference: patcher.AddDecodableReference(createdObject),
							},
						},
					}
				})
			}),
		),
	)
	defer addsListBuilder.Discard()

	for _, add := range a.adds {
		leaf, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.Args_Leaf_Add_Leaf, error) {
			leaf := &model_analysis_pb.Args_Leaf_Add_Leaf{
				StartWith:         add.startWith,
				ExpandDirectories: add.expandDirectories,
				FormatEach:        add.formatEach,
				OmitIfEmpty:       add.omitIfEmpty,
				Uniquify:          add.uniquify,
			}

			values, err := model_starlark.EncodeValue(thread, add.values, nil, nil, options)
			if err != nil {
				return nil, err
			}
			leaf.Values = values.Merge(patcher)

			if add.mapEach != nil {
				mapEach, err := model_starlark.EncodeFunction(thread, add.mapEach, path, options)
				if err != nil {
					return nil, err
				}
				leaf.MapEach = mapEach.Merge(patcher)
			}

			add.setStyle(leaf)
			return leaf, nil
		})
		if err != nil {
			return model_core.PatchedMessage[*model_analysis_pb.Args_Leaf, TMetadata]{}, err
		}

		if err := addsListBuilder.PushChild(
			model_core.NewPatchedMessage(
				&model_analysis_pb.Args_Leaf_Add{
					Level: &model_analysis_pb.Args_Leaf_Add_Leaf_{
						Leaf: leaf.Message,
					},
				},
				leaf.Patcher,
			),
		); err != nil {
			return model_core.PatchedMessage[*model_analysis_pb.Args_Leaf, TMetadata]{}, err
		}
	}
	addsList, err := addsListBuilder.FinalizeList()
	if err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.Args_Leaf, TMetadata]{}, err
	}

	return model_core.NewPatchedMessage(
		&model_analysis_pb.Args_Leaf{
			Adds:         addsList.Message,
			UseParamFile: useParamFile,
		},
		addsList.Patcher,
	), nil
}

func (a *args[TReference, TMetadata]) doAdd(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	var startWith *wrapperspb.StringValue
	var value eval.Value
	valueUnpackerInto := unpack.Or([]unpack.UnpackerInto[eval.Value]{
		unpack.Canonicalize(unpack.String),
		unpack.Canonicalize(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
		unpack.Canonicalize(unpack.Type[model_starlark.Label[TReference, TMetadata]]("Label")),
	})
	switch len(args.Positional) {
	case 1:
		if err := eval.UnpackArgs(
			thread, b.Name(), eval.Args{Positional: args.Positional},
			"value", unpack.Bind(&value, valueUnpackerInto),
		); err != nil {
			return nil, err
		}
	case 2:
		var argName string
		if err := eval.UnpackArgs(
			thread, b.Name(), eval.Args{Positional: args.Positional},
			"arg_name", unpack.Bind(&argName, unpack.String),
			"value", unpack.Bind(&value, valueUnpackerInto),
		); err != nil {
			return nil, err
		}
		startWith = &wrapperspb.StringValue{
			Value: argName,
		}
	default:
		return nil, fmt.Errorf("%s: got %d positional arguments, want 1 or 2", b.Name(), len(args.Positional))
	}

	format := "%s"
	if err := eval.UnpackArgs(
		thread, b.Name(), eval.Args{Named: args.Named},
		"format?", unpack.Bind(&format, unpack.String),
	); err != nil {
		return nil, err
	}

	a.adds = append(a.adds, argsAdd[TReference, TMetadata]{
		startWith:  startWith,
		values:     eval.NewList([]eval.Value{value}),
		formatEach: format,
		setStyle: func(leaf *model_analysis_pb.Args_Leaf_Add_Leaf) {
			leaf.Style = &model_analysis_pb.Args_Leaf_Add_Leaf_Separate_{
				Separate: &model_analysis_pb.Args_Leaf_Add_Leaf_Separate{},
			}
		},
	})
	return a, nil
}

func (args[TReference, TMetadata]) doAddAllJoinedParseArgs(thread *eval.Thread, b *eval.Builtin, args eval.Args) (startWith *string, values eval.Value, err error) {
	valuesUnpackerInto := unpack.Or([]unpack.UnpackerInto[eval.Value]{
		unpack.Canonicalize(unpack.List(unpack.Any)),
		unpack.Canonicalize(unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset")),
	})
	switch len(args.Positional) {
	case 1:
		if err := eval.UnpackArgs(
			thread, b.Name(), eval.Args{Positional: args.Positional},
			"values", unpack.Bind(&values, valuesUnpackerInto),
		); err != nil {
			return nil, nil, err
		}
	case 2:
		if err := eval.UnpackArgs(
			thread, b.Name(), eval.Args{Positional: args.Positional},
			"arg_name", unpack.Bind(&startWith, unpack.Pointer(unpack.String)),
			"values", unpack.Bind(&values, valuesUnpackerInto),
		); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, fmt.Errorf("%s: got %d positional arguments, want 1 or 2", b.Name(), len(args.Positional))
	}
	return startWith, values, nil
}

func (a *args[TReference, TMetadata]) doAddAll(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	startWith, values, err := a.doAddAllJoinedParseArgs(thread, b, args)
	if err != nil {
		return nil, err
	}

	var mapEach eval.Function
	formatEach := "%s"
	var beforeEachStr *string
	omitIfEmpty := true
	uniquify := false
	expandDirectories := true
	var terminateWithStr *string
	allowClosure := false
	if err := eval.UnpackArgs(
		thread, b.Name(), eval.Args{Named: args.Named},
		"map_each?", unpack.Bind(&mapEach, unpack.Type[eval.Function]("function")),
		"format_each?", unpack.Bind(&formatEach, unpack.IfNotNone(unpack.String)),
		"before_each?", unpack.Bind(&beforeEachStr, unpack.IfNotNone(unpack.Pointer(unpack.String))),
		"omit_if_empty?", unpack.Bind(&omitIfEmpty, unpack.Bool),
		"uniquify?", unpack.Bind(&uniquify, unpack.Bool),
		"expand_directories?", unpack.Bind(&expandDirectories, unpack.Bool),
		"terminate_with?", unpack.Bind(&terminateWithStr, unpack.IfNotNone(unpack.Pointer(unpack.String))),
		"allow_closure?", unpack.Bind(&allowClosure, unpack.Bool),
	); err != nil {
		return nil, err
	}

	var beforeEach *wrapperspb.StringValue
	if beforeEachStr != nil {
		beforeEach = &wrapperspb.StringValue{
			Value: *beforeEachStr,
		}
	}
	var terminateWith *wrapperspb.StringValue
	if terminateWithStr != nil {
		terminateWith = &wrapperspb.StringValue{
			Value: *terminateWithStr,
		}
	}
	add := argsAdd[TReference, TMetadata]{
		values:            values,
		expandDirectories: expandDirectories,
		mapEach:           mapEach,
		formatEach:        formatEach,
		omitIfEmpty:       omitIfEmpty,
		uniquify:          uniquify,
		setStyle: func(leaf *model_analysis_pb.Args_Leaf_Add_Leaf) {
			leaf.Style = &model_analysis_pb.Args_Leaf_Add_Leaf_Separate_{
				Separate: &model_analysis_pb.Args_Leaf_Add_Leaf_Separate{
					BeforeEach:    beforeEach,
					TerminateWith: terminateWith,
				},
			}
		},
	}
	if startWith != nil {
		add.startWith = &wrapperspb.StringValue{
			Value: *startWith,
		}
	}
	a.adds = append(a.adds, add)
	return a, nil
}

func (a *args[TReference, TMetadata]) doAddJoined(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	startWith, values, err := a.doAddAllJoinedParseArgs(thread, b, args)
	if err != nil {
		return nil, err
	}

	var joinWith string
	var mapEach eval.Function
	formatEach := "%s"
	formatJoined := "%s"
	omitIfEmpty := true
	uniquify := false
	expandDirectories := true
	allowClosure := false
	if err := eval.UnpackArgs(
		thread, b.Name(), eval.Args{Named: args.Named},
		"join_with?", unpack.Bind(&joinWith, unpack.String),
		"map_each?", unpack.Bind(&mapEach, unpack.Type[eval.Function]("function")),
		"format_each?", unpack.Bind(&formatEach, unpack.IfNotNone(unpack.String)),
		"format_joined?", unpack.Bind(&formatJoined, unpack.IfNotNone(unpack.String)),
		"omit_if_empty?", unpack.Bind(&omitIfEmpty, unpack.Bool),
		"uniquify?", unpack.Bind(&uniquify, unpack.Bool),
		"expand_directories?", unpack.Bind(&expandDirectories, unpack.Bool),
		"allow_closure?", unpack.Bind(&allowClosure, unpack.Bool),
	); err != nil {
		return nil, err
	}

	add := argsAdd[TReference, TMetadata]{
		values:            values,
		expandDirectories: expandDirectories,
		mapEach:           mapEach,
		formatEach:        formatEach,
		omitIfEmpty:       omitIfEmpty,
		uniquify:          uniquify,
		setStyle: func(leaf *model_analysis_pb.Args_Leaf_Add_Leaf) {
			leaf.Style = &model_analysis_pb.Args_Leaf_Add_Leaf_Joined_{
				Joined: &model_analysis_pb.Args_Leaf_Add_Leaf_Joined{
					JoinWith:     joinWith,
					FormatJoined: formatJoined,
				},
			}
		},
	}
	if startWith != nil {
		add.startWith = &wrapperspb.StringValue{
			Value: *startWith,
		}
	}
	a.adds = append(a.adds, add)
	return a, nil
}

func (a *args[TReference, TMetadata]) doSetParamFileFormat(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	var format string
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"format", unpack.Bind(&format, unpack.String),
	); err != nil {
		return nil, err
	}
	switch format {
	case "multiline":
		a.paramFileFormat = model_analysis_pb.Args_Leaf_UseParamFile_MULTILINE
	case "shell":
		a.paramFileFormat = model_analysis_pb.Args_Leaf_UseParamFile_SHELL
	case "flag_per_line":
		a.paramFileFormat = model_analysis_pb.Args_Leaf_UseParamFile_FLAG_PER_LINE
	default:
		return nil, fmt.Errorf("unknown param file format %#v", format)
	}
	return a, nil
}

func (a *args[TReference, TMetadata]) doUseParamFile(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args.Positional))
	}
	var paramFileArg string
	useAlways := false
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"param_file_arg", unpack.Bind(&paramFileArg, unpack.String),
		"use_always?", unpack.Bind(&useAlways, unpack.Bool),
	); err != nil {
		return nil, err
	}
	a.useParamFile = &argsUseParamFile{
		paramFileArg: paramFileArg,
		useAlways:    useAlways,
	}
	return a, nil
}

type subruleContext[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	ruleContext *ruleContext[TReference, TMetadata]
}

var _ eval.HasAttrs = (*subruleContext[object.GlobalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (sc *subruleContext[TReference, TMetadata]) Repr(_ *eval.Thread, printer *eval.Printer) error {
	printer.WriteString(fmt.Sprintf("<subrule_ctx for %s>", sc.ruleContext.targetLabel.String()))
	return nil
}

func (subruleContext[TReference, TMetadata]) Type() string {
	return "subrule_ctx"
}

func (subruleContext[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (sc *subruleContext[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	// TODO: Add subrule_ctx.toolchains.
	rc := sc.ruleContext
	switch name {
	case "actions":
		return &ruleContextActions[TReference, TMetadata]{
			ruleContext: rc,
		}, nil
	case "label":
		return model_starlark.NewLabel[TReference, TMetadata](rc.targetLabel.AsResolved()), nil
	default:
		return nil, eval.NoSuchAttrError{Value: sc, Name: name}
	}
}

var subruleContextAttrNames = []string{
	"actions",
	"label",
}

func (subruleContext[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	return subruleContextAttrNames, nil
}
