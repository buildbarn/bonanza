package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/glob"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

var buildDotBazelTargetNames = []label.TargetName{
	util.Must(label.NewTargetName("BUILD.bazel")),
	util.Must(label.NewTargetName("BUILD")),
}

func (c *baseComputer[TReference, TMetadata]) ComputePackageValue(ctx context.Context, key *model_analysis_pb.Package_Key, e PackageEnvironment[TReference, TMetadata]) (PatchedPackageValue[TMetadata], error) {
	canonicalPackage, err := label.NewCanonicalPackage(key.Label)
	if err != nil {
		return PatchedPackageValue[TMetadata]{}, fmt.Errorf("invalid package label: %w", err)
	}
	if len(key.MacroInstanceName) > 0 {
		value, err := c.computeMacroInstancePackageValue(ctx, canonicalPackage, key.MacroInstanceName, e)
		if err != nil && !errors.Is(err, evaluation.ErrMissingDependency) {
			return PatchedPackageValue[TMetadata]{}, fmt.Errorf("macro instance %#v: %w", strings.Join(key.MacroInstanceName, ""), err)
		}
		return value, err
	}
	return c.computeTopLevelPackageValue(ctx, canonicalPackage, e)
}

// computeTopLevelPackageValue computes the value of the Package key
// for the top level of a package. It evaluates the package's
// BUILD.bazel file to obtain the targets and macro instances declared
// within.
func (c *baseComputer[TReference, TMetadata]) computeTopLevelPackageValue(ctx context.Context, canonicalPackage label.CanonicalPackage, e PackageEnvironment[TReference, TMetadata]) (PatchedPackageValue[TMetadata], error) {
	canonicalRepo := canonicalPackage.GetCanonicalRepo()

	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	repoDefaultAttrsValue := e.GetRepoDefaultAttrsValue(&model_analysis_pb.RepoDefaultAttrs_Key{
		CanonicalRepo: canonicalRepo.String(),
	})
	fileReader, gotFileReader := e.GetFileReaderValue(&model_analysis_pb.FileReader_Key{})
	if !allBuiltinsModulesNames.IsSet() || !repoDefaultAttrsValue.IsSet() || !gotFileReader {
		return PatchedPackageValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	builtinsModuleNames := allBuiltinsModulesNames.Message.BuiltinsModuleNames
	thread := c.newStarlarkThread(ctx, e, builtinsModuleNames)
	buildFileBuiltins, err := c.getBuildFileBuiltins(thread, e, builtinsModuleNames)
	if err != nil {
		return PatchedPackageValue[TMetadata]{}, err
	}

	for _, buildFileName := range buildDotBazelTargetNames {
		buildFileProperties := e.GetFilePropertiesValue(&model_analysis_pb.FileProperties_Key{
			CanonicalRepo: canonicalRepo.String(),
			Path:          canonicalPackage.AppendTargetName(buildFileName).GetRepoRelativePath(),
		})
		if !buildFileProperties.IsSet() {
			return PatchedPackageValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		if buildFileProperties.Message.Exists == nil {
			continue
		}

		buildFileLabel := canonicalPackage.AppendTargetName(buildFileName)
		buildFileContentsEntry, err := model_filesystem.NewFileContentsEntryFromProto(
			model_core.Nested(buildFileProperties, buildFileProperties.Message.Exists.Contents),
		)
		if err != nil {
			return PatchedPackageValue[TMetadata]{}, fmt.Errorf("invalid contents for file %#v: %w", buildFileLabel.String(), err)
		}
		buildFileData, err := fileReader.FileReadAll(ctx, buildFileContentsEntry, 1<<20)
		if err != nil {
			return PatchedPackageValue[TMetadata]{}, err
		}

		_, program, err := starlark.SourceProgramOptions(
			&syntax.FileOptions{
				Set: true,
			},
			buildFileLabel.String(),
			buildFileData,
			buildFileBuiltins.Has,
		)
		if err != nil {
			return PatchedPackageValue[TMetadata]{}, fmt.Errorf("failed to load %#v: %w", buildFileLabel.String(), err)
		}

		if err := c.preloadBzlGlobals(e, canonicalPackage, program, builtinsModuleNames); err != nil {
			return PatchedPackageValue[TMetadata]{}, err
		}

		thread.SetLocal(model_starlark.CanonicalPackageKey, canonicalPackage)
		valueEncodingOptions := c.getValueEncodingOptions(ctx, e, nil)
		thread.SetLocal(model_starlark.ValueEncodingOptionsKey, valueEncodingOptions)
		thread.SetLocal(model_starlark.GlobExpanderKey, func(includePatterns, excludePatterns []string, includeDirectories bool) ([]string, error) {
			nfa, err := glob.NewNFAFromPatterns(includePatterns, excludePatterns)
			if err != nil {
				return nil, err
			}
			globValue := e.GetGlobValue(&model_analysis_pb.Glob_Key{
				Package:            canonicalPackage.String(),
				Pattern:            nfa.Bytes(),
				IncludeDirectories: includeDirectories,
			})
			if !globValue.IsSet() {
				return nil, evaluation.ErrMissingDependency
			}
			return globValue.Message.MatchedPaths, nil
		})

		targetRegistrar := model_starlark.NewTargetRegistrar(
			ctx,
			c.getValueObjectEncoder(),
			c.getInlinedTreeOptions(),
			e,
			model_core.Nested(repoDefaultAttrsValue, repoDefaultAttrsValue.Message.InheritableAttrs),
		)
		defer targetRegistrar.Discard()
		thread.SetLocal(model_starlark.TargetRegistrarKey, targetRegistrar)

		thread.SetLocal(model_starlark.GlobalResolverKey, c.newGlobalResolver(ctx, e, builtinsModuleNames))

		// Execute the BUILD.bazel file, so that all targets
		// contained within are instantiated.
		if _, err := program.Init(thread, buildFileBuiltins); err != nil {
			var evalErr *starlark.EvalError
			if !errors.Is(err, evaluation.ErrMissingDependency) && errors.As(err, &evalErr) {
				return PatchedPackageValue[TMetadata]{}, errors.New(evalErr.Backtrace())
			}
			return PatchedPackageValue[TMetadata]{}, err
		}

		if err := targetRegistrar.Finalize(canonicalPackage, valueEncodingOptions); err != nil {
			return PatchedPackageValue[TMetadata]{}, err
		}

		// Store all targets and macro instances in B-trees.
		return c.buildPackageDeclarations(ctx, e, targetRegistrar, valueEncodingOptions)
	}

	return PatchedPackageValue[TMetadata]{}, errors.New("BUILD.bazel does not exist")
}

// newPackageTargetsTreeBuilder creates a B-tree builder for lists of
// targets, as stored in the values of Package and PackageTargets.
func (c *baseComputer[TReference, TMetadata]) newPackageTargetsTreeBuilder(ctx context.Context, capturer model_core.CreatedObjectCapturer[TMetadata]) btree.Builder[*model_analysis_pb.Package_Value_Target, TMetadata] {
	// TODO: Use a proper encoder!
	return btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			/* minimumSizeBytes = */ 32*1024,
			/* maximumSizeBytes = */ 128*1024,
			/* isParent = */ func(target *model_analysis_pb.Package_Value_Target) bool {
				return target.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			c.getValueObjectEncoder(),
			c.referenceFormat,
			/* parentNodeComputer = */ btree.Capturing(ctx, capturer, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.Package_Value_Target, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.Package_Value_Target, TMetadata] {
				var firstName, lastName string
				switch firstElement := childNodes.Message[0].Level.(type) {
				case *model_analysis_pb.Package_Value_Target_Leaf:
					firstName = firstElement.Leaf.Name
				case *model_analysis_pb.Package_Value_Target_Parent_:
					firstName = firstElement.Parent.FirstName
				}
				switch lastElement := childNodes.Message[len(childNodes.Message)-1].Level.(type) {
				case *model_analysis_pb.Package_Value_Target_Leaf:
					lastName = lastElement.Leaf.Name
				case *model_analysis_pb.Package_Value_Target_Parent_:
					lastName = lastElement.Parent.LastName
				}
				return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.Package_Value_Target {
					return &model_analysis_pb.Package_Value_Target{
						Level: &model_analysis_pb.Package_Value_Target_Parent_{
							Parent: &model_analysis_pb.Package_Value_Target_Parent{
								Reference: patcher.AddDecodableReference(createdObject),
								FirstName: firstName,
								LastName:  lastName,
							},
						},
					}
				})
			}),
		),
	)
}

// buildPackageDeclarations extracts all targets and macro instances
// from a TargetRegistrar, and stores them in B-trees that are sorted by
// name.
func (c *baseComputer[TReference, TMetadata]) buildPackageDeclarations(ctx context.Context, capturer model_core.CreatedObjectCapturer[TMetadata], targetRegistrar *model_starlark.TargetRegistrar[TReference, TMetadata], valueEncodingOptions *model_starlark.ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_analysis_pb.Package_Value, TMetadata], error) {
	targetsBuilder := c.newPackageTargetsTreeBuilder(ctx, capturer)
	defer targetsBuilder.Discard()
	for _, name := range targetRegistrar.GetTargetNames() {
		target := targetRegistrar.GetAndRemoveTarget(name)
		if err := targetsBuilder.PushChild(model_core.NewPatchedMessage(
			&model_analysis_pb.Package_Value_Target{
				Level: &model_analysis_pb.Package_Value_Target_Leaf{
					Leaf: &model_starlark_pb.Target{
						Name:       name,
						Definition: target.Message,
					},
				},
			},
			target.Patcher,
		)); err != nil {
			return model_core.PatchedMessage[*model_analysis_pb.Package_Value, TMetadata]{}, err
		}
	}
	targets, err := targetsBuilder.FinalizeList()
	if err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.Package_Value, TMetadata]{}, err
	}

	macroExpansionContextsBuilder := model_starlark.NewMacroExpansionContextListBuilder(valueEncodingOptions)
	defer macroExpansionContextsBuilder.Discard()
	for _, name := range targetRegistrar.GetMacroExpansionContextNames() {
		macroExpansionContext := targetRegistrar.GetAndRemoveMacroExpansionContext(name)
		if err := macroExpansionContextsBuilder.PushChild(model_core.NewPatchedMessage(
			&model_starlark_pb.MacroExpansionContext_Entry{
				Level: &model_starlark_pb.MacroExpansionContext_Entry_Leaf{
					Leaf: macroExpansionContext.Message,
				},
			},
			macroExpansionContext.Patcher,
		)); err != nil {
			targets.Discard()
			return model_core.PatchedMessage[*model_analysis_pb.Package_Value, TMetadata]{}, err
		}
	}
	macroExpansionContexts, err := macroExpansionContextsBuilder.FinalizeList()
	if err != nil {
		targets.Discard()
		return model_core.PatchedMessage[*model_analysis_pb.Package_Value, TMetadata]{}, err
	}

	targets.Patcher.Merge(macroExpansionContexts.Patcher)
	return model_core.NewPatchedMessage(
		&model_analysis_pb.Package_Value{
			Targets:                targets.Message,
			MacroExpansionContexts: macroExpansionContexts.Message,
		},
		targets.Patcher,
	), nil
}

// newGlobalResolver creates a function that can be used to look up the
// values of globals declared in .bzl files. It is used to obtain the
// definitions of rules and macros that are referenced by identifier.
func (c *baseComputer[TReference, TMetadata]) newGlobalResolver(ctx context.Context, e CompiledBzlFileValueGetter[TReference], builtinsModuleNames []string) model_starlark.GlobalResolver[TReference] {
	return func(identifier label.CanonicalStarlarkIdentifier) (model_core.Message[*model_starlark_pb.Value, TReference], error) {
		canonicalLabel := identifier.GetCanonicalLabel()
		compiledBzlFile := e.GetCompiledBzlFileValue(&model_analysis_pb.CompiledBzlFile_Key{
			Label:               canonicalLabel.String(),
			BuiltinsModuleNames: trimBuiltinModuleNames(builtinsModuleNames, canonicalLabel.GetCanonicalRepo().GetModuleInstance().GetModule()),
		})
		if !compiledBzlFile.IsSet() {
			return model_core.Message[*model_starlark_pb.Value, TReference]{}, evaluation.ErrMissingDependency
		}
		return model_starlark.GetStructFieldValue(
			ctx,
			c.valueReaders.List,
			model_core.Nested(compiledBzlFile, compiledBzlFile.Message.CompiledProgram.GetGlobals()),
			identifier.GetStarlarkIdentifier().String(),
		)
	}
}

// decodeMacroAttrValue decodes the value of an attribute of a macro
// instance, so that it can be provided to the macro's implementation
// function. As with Bazel, values of configurable attributes are
// provided as select() expressions, even if no select() expression was
// used to construct the value. None values are not wrapped.
func decodeMacroAttrValue[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	valueParts model_core.Message[[]*model_starlark_pb.Select_Group, TReference],
	concatenationOperator syntax.Token,
	defaultValue model_core.Message[*model_starlark_pb.Value, TReference],
	options *model_starlark.ValueDecodingOptions[TReference],
) (starlark.Value, error) {
	groups := valueParts.Message
	if len(groups) == 1 && len(groups[0].Conditions) == 0 {
		if noMatchValue, ok := groups[0].NoMatch.(*model_starlark_pb.Select_Group_NoMatchValue); ok {
			if _, ok := noMatchValue.NoMatchValue.Kind.(*model_starlark_pb.Value_None); ok {
				// No value provided. Use the default
				// value of the attribute.
				if !defaultValue.IsSet() {
					return starlark.None, nil
				}
				if _, ok := defaultValue.Message.Kind.(*model_starlark_pb.Value_None); ok {
					return starlark.None, nil
				}
				return decodeMacroSelectValue[TReference, TMetadata](
					model_core.Nested(defaultValue, []*model_starlark_pb.Select_Group{{
						NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{
							NoMatchValue: defaultValue.Message,
						},
					}}),
					concatenationOperator,
					options,
				)
			}
		}
	}
	return decodeMacroSelectValue[TReference, TMetadata](valueParts, concatenationOperator, options)
}

func decodeMacroSelectValue[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	groups model_core.Message[[]*model_starlark_pb.Select_Group, TReference],
	concatenationOperator syntax.Token,
	options *model_starlark.ValueDecodingOptions[TReference],
) (starlark.Value, error) {
	encodedSelect := &model_starlark_pb.Select{
		Groups: groups.Message,
	}
	if len(groups.Message) > 1 {
		switch concatenationOperator {
		case syntax.PIPE:
			encodedSelect.ConcatenationOperator = model_starlark_pb.Select_PIPE
		case syntax.PLUS:
			encodedSelect.ConcatenationOperator = model_starlark_pb.Select_PLUS
		default:
			return nil, errors.New("value consists of multiple parts, even though the attribute type cannot be concatenated")
		}
	}
	return model_starlark.DecodeValue[TReference, TMetadata](
		model_core.Nested(groups, &model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Select{
				Select: encodedSelect,
			},
		}),
		/* currentIdentifier = */ nil,
		options,
	)
}

// computeMacroInstancePackageValue computes the value of the Package
// key for a macro instance. It invokes the macro's implementation
// function to obtain the targets and macro instances declared by the
// macro instance.
func (c *baseComputer[TReference, TMetadata]) computeMacroInstancePackageValue(ctx context.Context, canonicalPackage label.CanonicalPackage, macroInstanceNameSuffixes []string, e PackageEnvironment[TReference, TMetadata]) (PatchedPackageValue[TMetadata], error) {
	macroInstanceName := strings.Join(macroInstanceNameSuffixes, "")

	// Look up the context for expanding the macro instance. Only
	// depending on this, as opposed to the declarations of the
	// macro instance's parent, ensures that changes to the parent
	// don't cause the macro instance to be expanded again.
	macroExpansionContextValue := e.GetMacroExpansionContextValue(&model_analysis_pb.MacroExpansionContext_Key{
		Package:           canonicalPackage.String(),
		MacroInstanceName: macroInstanceNameSuffixes,
	})
	if !macroExpansionContextValue.IsSet() {
		return PatchedPackageValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	macroExpansionContextDefinition := model_core.Nested(macroExpansionContextValue, macroExpansionContextValue.Message.Definition)
	rootMacroInstanceDefinition := model_core.Nested(macroExpansionContextDefinition, macroExpansionContextDefinition.Message.GetRootMacroInstanceDefinition())
	if rootMacroInstanceDefinition.Message.GetInheritableAttrs().GetVisibility() == nil {
		return PatchedPackageValue[TMetadata]{}, errors.New("does not have a visibility")
	}
	for _, suffix := range macroExpansionContextDefinition.Message.ReferencedTargetNameSuffixes {
		if name := macroInstanceName + suffix; !model_starlark.IsValidMacroDeclarationName(macroInstanceName, name) {
			return PatchedPackageValue[TMetadata]{}, fmt.Errorf("invalid referenced target name suffix %#v", suffix)
		}
	}

	macroIdentifier, err := label.NewCanonicalStarlarkIdentifier(rootMacroInstanceDefinition.Message.MacroIdentifier)
	if err != nil {
		return PatchedPackageValue[TMetadata]{}, fmt.Errorf("invalid macro identifier %#v: %w", rootMacroInstanceDefinition.Message.MacroIdentifier, err)
	}
	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	macroValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
		Identifier: macroIdentifier.String(),
	})
	if !allBuiltinsModulesNames.IsSet() || !macroValue.IsSet() {
		return PatchedPackageValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	macroDefinition, err := model_starlark.GetMacroDefinition(model_core.Nested(macroValue, macroValue.Message.Global))
	if err != nil {
		return PatchedPackageValue[TMetadata]{}, fmt.Errorf("identifier %#v: %w", macroIdentifier.String(), err)
	}

	builtinsModuleNames := allBuiltinsModulesNames.Message.BuiltinsModuleNames
	thread := c.newStarlarkThread(ctx, e, builtinsModuleNames)
	thread.SetLocal(model_starlark.CanonicalPackageKey, canonicalPackage)
	valueEncodingOptions := c.getValueEncodingOptions(ctx, e, nil)
	thread.SetLocal(model_starlark.ValueEncodingOptionsKey, valueEncodingOptions)
	thread.SetLocal(model_starlark.GlobalResolverKey, c.newGlobalResolver(ctx, e, builtinsModuleNames))

	// Targets declared by the macro are registered against a
	// separate TargetRegistrar, which enforces that their names
	// are prefixed by the name of the macro instance. Targets that
	// are referenced by the caller of the macro are registered
	// implicitly.
	targetRegistrar := model_starlark.NewMacroTargetRegistrar(
		ctx,
		c.getValueObjectEncoder(),
		c.getInlinedTreeOptions(),
		e,
		macroInstanceName,
		macroIdentifier.GetCanonicalLabel().GetCanonicalPackage(),
		macroExpansionContextDefinition,
	)
	defer targetRegistrar.Discard()
	thread.SetLocal(model_starlark.TargetRegistrarKey, targetRegistrar)

	// Macro instances declared by the caller of the macro whose
	// namespace is contained within the namespace of this macro
	// instance are treated as if this macro instance declared them.
	var errIter error
	for entry := range btree.AllLeaves(
		ctx,
		c.macroInstanceEntryReader,
		model_core.Nested(macroExpansionContextDefinition, macroExpansionContextDefinition.Message.NestedMacroInstances),
		func(entry model_core.Message[*model_starlark_pb.MacroInstance_Entry, TReference]) (*model_core_pb.DecodableReference, error) {
			if level, ok := entry.Message.Level.(*model_starlark_pb.MacroInstance_Entry_Parent_); ok {
				return level.Parent.Reference, nil
			}
			return nil, nil
		},
		&errIter,
	) {
		level, ok := entry.Message.Level.(*model_starlark_pb.MacroInstance_Entry_Leaf)
		if !ok {
			return PatchedPackageValue[TMetadata]{}, errors.New("not a valid leaf entry")
		}
		if err := targetRegistrar.RegisterNestedMacroInstance(model_core.Patch(e, model_core.Nested(entry, level.Leaf))); err != nil {
			return PatchedPackageValue[TMetadata]{}, err
		}
	}
	if errIter != nil {
		return PatchedPackageValue[TMetadata]{}, errIter
	}

	// Construct the arguments of the implementation function.
	valueDecodingOptions := thread.Local(model_starlark.ValueDecodingOptionsKey).(*model_starlark.ValueDecodingOptions[TReference])
	kwargs := []starlark.Tuple{
		{starlark.String("name"), starlark.String(macroInstanceName)},
		{starlark.String("visibility"), model_starlark.MacroVisibility{}},
	}
	publicAttrValues := rootMacroInstanceDefinition.Message.PublicAttrValues
	for _, namedAttr := range macroDefinition.Message.Attrs {
		attr := namedAttr.Attr
		if attr == nil {
			return PatchedPackageValue[TMetadata]{}, fmt.Errorf("attr %#v: missing message", namedAttr.Name)
		}
		var defaultValue model_core.Message[*model_starlark_pb.Value, TReference]
		if attr.Default != nil {
			defaultValue = model_core.Nested(macroDefinition, attr.Default)
		}
		var value starlark.Value
		if strings.HasPrefix(namedAttr.Name, "_") {
			// Private attributes cannot be set by the
			// caller. Provide the default value.
			value = starlark.None
			if defaultValue.IsSet() {
				value, err = model_starlark.DecodeValue[TReference, TMetadata](defaultValue, nil, valueDecodingOptions)
				if err != nil {
					return PatchedPackageValue[TMetadata]{}, fmt.Errorf("attr %#v: %w", namedAttr.Name, err)
				}
			}
		} else {
			if len(publicAttrValues) == 0 {
				return PatchedPackageValue[TMetadata]{}, errors.New("has fewer public attr values than the macro definition has public attrs")
			}
			publicAttrValue := publicAttrValues[0]
			publicAttrValues = publicAttrValues[1:]

			attrType, err := model_starlark.DecodeAttrType[TReference, TMetadata](model_core.Nested(macroDefinition, attr))
			if err != nil {
				return PatchedPackageValue[TMetadata]{}, fmt.Errorf("attr %#v: %w", namedAttr.Name, err)
			}
			value, err = decodeMacroAttrValue[TReference, TMetadata](
				model_core.Nested(rootMacroInstanceDefinition, publicAttrValue.ValueParts),
				attrType.GetCanonicalizer(canonicalPackage).GetConcatenationOperator(),
				defaultValue,
				valueDecodingOptions,
			)
			if err != nil {
				return PatchedPackageValue[TMetadata]{}, fmt.Errorf("attr %#v: %w", namedAttr.Name, err)
			}
		}
		kwargs = append(kwargs, starlark.Tuple{starlark.String(namedAttr.Name), value})
	}
	if len(publicAttrValues) != 0 {
		return PatchedPackageValue[TMetadata]{}, errors.New("has more public attr values than the macro definition has public attrs")
	}

	// Invoke the implementation function, so that all targets and
	// macro instances declared by the macro are registered.
	returnValue, err := starlark.Call(
		thread,
		model_starlark.NewNamedFunction(model_starlark.NewProtoNamedFunctionDefinition[TReference, TMetadata](
			model_core.Nested(macroDefinition, macroDefinition.Message.Implementation),
		)),
		/* args = */ nil,
		kwargs,
	)
	if err != nil {
		var evalErr *starlark.EvalError
		if !errors.Is(err, evaluation.ErrMissingDependency) && errors.As(err, &evalErr) {
			return PatchedPackageValue[TMetadata]{}, errors.New(evalErr.Backtrace())
		}
		return PatchedPackageValue[TMetadata]{}, err
	}
	if returnValue != starlark.None {
		return PatchedPackageValue[TMetadata]{}, fmt.Errorf("implementation function of macro %#v returned a value of type %s, while macros may not return values", macroIdentifier.String(), returnValue.Type())
	}

	// Test suites declared by the macro that don't list any tests
	// contain the tests declared by the same macro instance.
	if err := targetRegistrar.Finalize(canonicalPackage, valueEncodingOptions); err != nil {
		return PatchedPackageValue[TMetadata]{}, err
	}

	return c.buildPackageDeclarations(ctx, e, targetRegistrar, valueEncodingOptions)
}

// PackageEnvironmentForTesting is used to generate mocks for testing
// ComputePackageValue().
type PackageEnvironmentForTesting PackageEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
