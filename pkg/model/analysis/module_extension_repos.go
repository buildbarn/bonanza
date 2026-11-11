package analysis

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"bonanza.build/pkg/label"
	"bonanza.build/pkg/model/chunking"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	"bonanza.build/pkg/moduledotbazel"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/proto"
)

type bazelModuleTag struct {
	tagClass        *model_starlark_pb.TagClass
	isDevDependency bool
	attrs           []eval.Value
}

var (
	_ eval.HasAttrs = bazelModuleTag{}
	_ eval.Value    = bazelModuleTag{}
)

// Repr writes the representation of the value.
func (bazelModuleTag) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<bazel_module_tag>")
	return nil
}

func (bazelModuleTag) Type() string {
	return "bazel_module_tag"
}

func (bazelModuleTag) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (t bazelModuleTag) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	attrs := t.tagClass.GetAttrs()
	if index, ok := sort.Find(
		len(attrs),
		func(i int) int { return strings.Compare(name, attrs[i].Name) },
	); ok {
		return t.attrs[index], nil
	}
	return nil, eval.NoSuchAttrError{Value: t, Name: name}
}

func (t bazelModuleTag) AttrNames(*eval.Thread) ([]string, error) {
	attrs := t.tagClass.GetAttrs()
	attrNames := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		attrNames = append(attrNames, attr.Name)
	}
	return attrNames, nil
}

var repoRulesExtensionName = util.Must(label.NewStarlarkIdentifier("_repo_rules"))

type useRepoRuleCapturingModuleDotBazelHandler[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	labelResolver         label.Resolver
	moduleRepo            label.CanonicalRepo
	ignoreDevDependencies bool
	valueEncodingOptions  *model_starlark.ValueEncodingOptions[TReference, TMetadata]

	repos map[string]model_core.PatchedMessage[*model_starlark_pb.Repo, TMetadata]
}

func (useRepoRuleCapturingModuleDotBazelHandler[TReference, TMetadata]) BazelDep(name label.Module, version *label.ModuleVersion, repoName label.ApparentRepo, devDependency bool) error {
	return nil
}

func (useRepoRuleCapturingModuleDotBazelHandler[TReference, TMetadata]) Module(name label.Module, repoName label.ApparentRepo, bazelCompatibility []string) error {
	return nil
}

func (useRepoRuleCapturingModuleDotBazelHandler[TReference, TMetadata]) RegisterExecutionPlatforms(platformTargetPatterns []label.ApparentTargetPattern, devDependency bool) error {
	return nil
}

func (useRepoRuleCapturingModuleDotBazelHandler[TReference, TMetadata]) RegisterToolchains(toolchainTargetPatterns []label.ApparentTargetPattern, devDependency bool) error {
	return nil
}

func (useRepoRuleCapturingModuleDotBazelHandler[TReference, TMetadata]) UseExtension(extensionBzlFile label.ApparentLabel, extensionName label.StarlarkIdentifier, devDependency, isolate bool) (moduledotbazel.ModuleExtensionProxy, error) {
	return moduledotbazel.NullModuleExtensionProxy, nil
}

func (h *useRepoRuleCapturingModuleDotBazelHandler[TReference, TMetadata]) UseRepoRule(repoRuleBzlFile label.ApparentLabel, repoRuleName label.StarlarkIdentifier) (moduledotbazel.RepoRuleProxy, error) {
	return func(thread *eval.Thread, name label.ApparentRepo, devDependency bool, attrs map[string]eval.Value) error {
		if devDependency && h.ignoreDevDependencies {
			return nil
		}

		canonicalRepoRuleBzlFile, err := label.Canonicalize(h.labelResolver, h.moduleRepo, repoRuleBzlFile)
		if err != nil {
			return err
		}

		attrValues, err := model_starlark.EncodeStructFields(thread, attrs, h.valueEncodingOptions)
		if err != nil {
			return err
		}

		nameStr := name.String()
		if _, ok := h.repos[nameStr]; ok {
			return fmt.Errorf("multiple repos declared with name %#v", nameStr)
		}
		h.repos[nameStr] = model_core.NewPatchedMessage(
			&model_starlark_pb.Repo{
				Name: nameStr,
				Definition: &model_starlark_pb.Repo_Definition{
					RepositoryRuleIdentifier: canonicalRepoRuleBzlFile.AppendStarlarkIdentifier(repoRuleName).String(),
					AttrValues:               attrValues.Message,
				},
			},
			attrValues.Patcher,
		)
		return nil
	}, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeModuleExtensionReposValue(ctx context.Context, key *model_analysis_pb.ModuleExtensionRepos_Key, e ModuleExtensionReposEnvironment[TReference, TMetadata]) (PatchedModuleExtensionReposValue[TMetadata], error) {
	starlarkValueEncoders, err := getStarlarkValueEncoders(e)
	if err != nil {
		return PatchedModuleExtensionReposValue[TMetadata]{}, err
	}
	valueReaders := &starlarkValueEncoders.Readers

	valueEncodingOptionsFactory, err := c.newValueEncodingOptionsFactory(e)
	if err != nil {
		return PatchedModuleExtensionReposValue[TMetadata]{}, err
	}

	// Store all repos in a B-tree.
	treeBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			util.Must(chunking.NewParametersFromProto(&model_chunking_pb.Parameters{
				MinimumSizeBytes: 32 * 1024,
				HorizonSizeBytes: 8 * 32 * 1024,
			}, c.referenceFormat)),
			/* isParent = */ func(repo *model_analysis_pb.ModuleExtensionRepos_Value_Repo) bool {
				return repo.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			getValueObjectEncoder(),
			c.referenceFormat,
			/* parentNodeComputer = */ btree.Capturing(ctx, e, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.ModuleExtensionRepos_Value_Repo, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.ModuleExtensionRepos_Value_Repo, TMetadata] {
				var firstName string
				switch firstElement := childNodes.Message[0].Level.(type) {
				case *model_analysis_pb.ModuleExtensionRepos_Value_Repo_Leaf:
					firstName = firstElement.Leaf.Name
				case *model_analysis_pb.ModuleExtensionRepos_Value_Repo_Parent_:
					firstName = firstElement.Parent.FirstName
				}
				return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ModuleExtensionRepos_Value_Repo {
					return &model_analysis_pb.ModuleExtensionRepos_Value_Repo{
						Level: &model_analysis_pb.ModuleExtensionRepos_Value_Repo_Parent_{
							Parent: &model_analysis_pb.ModuleExtensionRepos_Value_Repo_Parent{
								Reference: patcher.AddDecodableReference(createdObject),
								FirstName: firstName,
							},
						},
					}
				})
			}),
		),
	)

	var repos map[string]model_core.PatchedMessage[*model_starlark_pb.Repo, TMetadata]
	moduleExtension, err := label.NewModuleExtension(key.ModuleExtension)
	if err != nil {
		return PatchedModuleExtensionReposValue[TMetadata]{}, fmt.Errorf("invalid module extension: %w", err)
	}
	if moduleExtension.GetExtensionName() == repoRulesExtensionName {
		// "_repo_rules" is used as a fictive extension name to
		// hold all repos declared using use_repo_rule() in
		// MODULE.bazel. Instead of attempting to invoke an
		// actual module extension, gather repos from the
		// MODULE.bazel file.
		rootModuleValue := e.GetRootModuleValue(&model_analysis_pb.RootModule_Key{})
		if !rootModuleValue.IsSet() {
			return PatchedModuleExtensionReposValue[TMetadata]{}, evaluation.ErrMissingDependency
		}

		moduleInstance := moduleExtension.GetModuleInstance()
		moduleRepo := moduleInstance.GetBareCanonicalRepo()
		handler := useRepoRuleCapturingModuleDotBazelHandler[TReference, TMetadata]{
			labelResolver: newLabelResolver(e),
			moduleRepo:    moduleRepo,
			ignoreDevDependencies: rootModuleValue.Message.IgnoreRootModuleDevDependencies ||
				moduleInstance.GetModule().String() != rootModuleValue.Message.RootModuleName,
			valueEncodingOptions: valueEncodingOptionsFactory.newValueEncodingOptions(ctx, nil),
			repos:                map[string]model_core.PatchedMessage[*model_starlark_pb.Repo, TMetadata]{},
		}

		if err := c.parseActiveModuleInstanceModuleDotBazel(
			ctx,
			moduleInstance,
			e,
			moduledotbazel.NewOverrideIgnoringRootHandler(&handler),
		); err != nil {
			return PatchedModuleExtensionReposValue[TMetadata]{}, err
		}
		repos = handler.repos
	} else {
		allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
		repoPlatform := e.GetRegisteredRepoPlatformValue(&model_analysis_pb.RegisteredRepoPlatform_Key{})
		if !allBuiltinsModulesNames.IsSet() || !repoPlatform.IsSet() {
			return PatchedModuleExtensionReposValue[TMetadata]{}, evaluation.ErrMissingDependency
		}

		// Resolve the module extension object that was declared within
		// Starlark code.
		usedModuleExtensionValue := e.GetUsedModuleExtensionValue(&model_analysis_pb.UsedModuleExtension_Key{
			ModuleExtension: moduleExtension.String(),
		})
		if !usedModuleExtensionValue.IsSet() {
			return PatchedModuleExtensionReposValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		usedModuleExtension := usedModuleExtensionValue.Message.ModuleExtension

		moduleExtensionIdentifierStr := usedModuleExtension.GetIdentifier()
		moduleExtensionIdentifier, err := label.NewCanonicalStarlarkIdentifier(moduleExtensionIdentifierStr)
		if err != nil {
			return PatchedModuleExtensionReposValue[TMetadata]{}, fmt.Errorf("invalid module extension identifier %#v: %w", moduleExtensionIdentifierStr, err)
		}
		moduleExtensionDefinitionValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
			Identifier: moduleExtensionIdentifier.String(),
		})
		if !moduleExtensionDefinitionValue.IsSet() {
			return PatchedModuleExtensionReposValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		v, ok := moduleExtensionDefinitionValue.Message.Global.GetKind().(*model_starlark_pb.Value_ModuleExtension)
		if !ok {
			return PatchedModuleExtensionReposValue[TMetadata]{}, fmt.Errorf("%#v is not a module extension", moduleExtensionIdentifier.String())
		}
		moduleExtensionDefinition := v.ModuleExtension

		thread, err := c.newStarlarkThread(ctx, e, starlarkValueEncoders, allBuiltinsModulesNames.Message.BuiltinsModuleNames)
		if err != nil {
			return PatchedModuleExtensionReposValue[TMetadata]{}, err
		}

		// Decode tags declared in all MODULE.bazel files belonging to
		// this module extension.
		moduleExtensionUsers := usedModuleExtension.GetUsers()
		modules := make([]eval.Value, 0, len(moduleExtensionUsers))
		listReader := valueReaders.List
		tagClassAttrTypes := make([][]model_starlark.AttrType[TReference, TMetadata], len(moduleExtensionDefinition.TagClasses))
		tagClassAttrDefaults := make([][]eval.Value, len(moduleExtensionDefinition.TagClasses))
		for _, user := range moduleExtensionUsers {
			moduleInstance, err := label.NewModuleInstance(user.ModuleInstance)
			if err != nil {
				return PatchedModuleExtensionReposValue[TMetadata]{}, fmt.Errorf("invalid module instance %#v: %w", user.ModuleInstance, err)
			}
			versionStr := ""
			if v, ok := moduleInstance.GetModuleVersion(); ok {
				versionStr = v.String()
			}

			usedTagClasses := user.TagClasses
			tagClasses := map[string]any{}
			for tagClassIndex, tagClass := range moduleExtensionDefinition.TagClasses {
				tagClassDefinition := tagClass.TagClass
				tagClassAttrs := tagClassDefinition.GetAttrs()

				var tags []eval.Value
				if len(usedTagClasses) > 0 && usedTagClasses[0].Name == tagClass.Name {
					declaredTags := usedTagClasses[0].Tags
					tags = make([]eval.Value, 0, len(declaredTags))
					for _, declaredTag := range declaredTags {
						var errIter error
						declaredAttrs := maps.Collect(
							model_starlark.AllStructFields(
								ctx,
								listReader,
								model_core.Nested(usedModuleExtensionValue, declaredTag.Attrs),
								&errIter,
							),
						)
						if errIter != nil {
							return PatchedModuleExtensionReposValue[TMetadata]{}, errIter
						}

						attrs := make([]eval.Value, 0, len(tagClassAttrs))
						for attrIndex, attr := range tagClassAttrs {
							if declaredValue, ok := declaredAttrs[attr.Name]; ok {
								value, err := model_starlark.DecodeValue[TReference, TMetadata](
									thread, declaredValue,
									/* currentIdentifier = */ nil,
								)
								if err != nil {
									return PatchedModuleExtensionReposValue[TMetadata]{}, fmt.Errorf("failed to decode value of attribute %#v of tag class %#v declared by module instance %#v", attr.Name, tagClass.Name, moduleInstance.String())
								}

								if len(tagClassAttrTypes[tagClassIndex]) != len(tagClassAttrs) {
									tagClassAttrTypes[tagClassIndex] = make([]model_starlark.AttrType[TReference, TMetadata], len(tagClassAttrs))
								}
								attrType := &tagClassAttrTypes[tagClassIndex][attrIndex]
								if *attrType == nil {
									// First time we see this tag class be
									// invoked with a value for this
									// attribute. Determine the attribute
									// type, so that the provided value can be
									// canonicalized.
									*attrType, err = model_starlark.DecodeAttrType[TReference, TMetadata](model_core.Nested(moduleExtensionDefinitionValue, attr.Attr))
									if err != nil {
										return PatchedModuleExtensionReposValue[TMetadata]{}, fmt.Errorf("failed to decode type of attribute %#v of tag class %#v", attr.Name, tagClass.Name)
									}
								}

								canonicalValue, err := (*attrType).GetCanonicalizer(
									moduleInstance.GetBareCanonicalRepo().GetRootPackage(),
								).Canonicalize(thread, value)
								if err != nil {
									return PatchedModuleExtensionReposValue[TMetadata]{}, fmt.Errorf("failed to canonicalize value of attribute %#v of tag class %#v declared by module instance %#v: %w", attr.Name, tagClass.Name, moduleInstance.String(), err)
								}
								attrs = append(attrs, canonicalValue)

								delete(declaredAttrs, attr.Name)
							} else if encodedDefaultValue := attr.Attr.GetDefault(); encodedDefaultValue != nil {
								// Tag didn't provide the attribute.
								// Use the default value.
								if len(tagClassAttrDefaults[tagClassIndex]) != len(tagClassAttrs) {
									tagClassAttrDefaults[tagClassIndex] = make([]eval.Value, len(tagClassAttrs))
								}
								defaultValue := &tagClassAttrDefaults[tagClassIndex][attrIndex]
								if *defaultValue == nil {
									// First time we see this tag class be
									// invoked without a value for this
									// attribute. Decode the default value.
									*defaultValue, err = model_starlark.DecodeValue[TReference, TMetadata](
										thread, model_core.Nested(moduleExtensionDefinitionValue, encodedDefaultValue),
										/* currentIdentifier = */ nil,
									)
									if err != nil {
										return PatchedModuleExtensionReposValue[TMetadata]{}, fmt.Errorf("failed to decode default value of attribute %#v of tag class %#v", attr.Name, tagClass.Name)
									}
								}
								attrs = append(attrs, *defaultValue)
							} else {
								return PatchedModuleExtensionReposValue[TMetadata]{}, fmt.Errorf("module instance %#v declares tag of class %#v with missing attribute %#v", moduleInstance.String(), tagClass.Name, attr.Name)
							}
						}
						if len(declaredAttrs) > 0 {
							return PatchedModuleExtensionReposValue[TMetadata]{}, fmt.Errorf(
								"module instance %#v declares tag of class %#v with unknown attribute %#v",
								moduleInstance.String(),
								tagClass.Name,
								slices.Min(slices.Collect(maps.Keys(declaredAttrs))),
							)
						}

						tags = append(tags, bazelModuleTag{
							tagClass:        tagClassDefinition,
							isDevDependency: declaredTag.IsDevDependency,
							attrs:           attrs,
						})
					}

					usedTagClasses = usedTagClasses[1:]
				}
				tagClasses[tagClass.Name] = eval.NewImmutableList(tags)
			}
			if len(usedTagClasses) > 0 {
				return PatchedModuleExtensionReposValue[TMetadata]{}, fmt.Errorf("module instance %#v uses unknown tag class %#v", moduleInstance.String(), usedTagClasses[0].Name)
			}

			modules = append(modules, model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
				"is_root": eval.Bool(user.IsRoot),
				"name":    eval.String(moduleInstance.GetModule().String()),
				"tags":    model_starlark.NewStructFromDict[TReference, TMetadata](nil, tagClasses),
				"version": eval.String(versionStr),
			}))
		}

		// Call into the implementation function to obtain a set of
		// repos declared by this module extension.
		thread.SetLocal(model_starlark.CanonicalPackageKey, moduleExtension.GetModuleInstance().GetBareCanonicalRepo().GetRootPackage())
		thread.SetLocal(model_starlark.ValueEncodingOptionsKey, valueEncodingOptionsFactory.newValueEncodingOptions(ctx, nil))

		repoRegistrar := model_starlark.NewRepoRegistrar[TMetadata]()
		thread.SetLocal(model_starlark.RepoRegistrarKey, repoRegistrar)

		identifierGenerator, err := c.getReferenceEqualIdentifierGenerator(model_core.NewSimpleMessage[TReference](proto.Message(key)))
		if err != nil {
			return PatchedModuleExtensionReposValue[TMetadata]{}, err
		}
		thread.SetLocal(model_starlark.ReferenceEqualIdentifierGeneratorKey, identifierGenerator)

		moduleContext, err := c.newModuleOrRepositoryContext(ctx, e, []path.Component{
			path.MustNewComponent("modextwd"),
			path.MustNewComponent(moduleExtension.String()),
		})
		if err != nil {
			return PatchedModuleExtensionReposValue[TMetadata]{}, err
		}
		defer moduleContext.release()

		repositoryOS, err := newRepositoryOS[TReference, TMetadata](thread, repoPlatform.Message)
		if err != nil {
			return PatchedModuleExtensionReposValue[TMetadata]{}, err
		}
		moduleCtx := model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
			// Fields shared with repository_ctx.
			"download":                           eval.NewBuiltin("module_ctx.download", moduleContext.doDownload),
			"download_and_extract":               eval.NewBuiltin("module_ctx.download_and_extract", moduleContext.doDownloadAndExtract),
			"execute":                            eval.NewBuiltin("module_ctx.execute", moduleContext.doExecute),
			"extract":                            eval.NewBuiltin("module_ctx.extract", moduleContext.doExtract),
			"file":                               eval.NewBuiltin("module_ctx.file", moduleContext.doFile),
			"getenv":                             eval.NewBuiltin("module_ctx.getenv", moduleContext.doGetenv),
			"os":                                 repositoryOS,
			"path":                               eval.NewBuiltin("module_ctx.path", moduleContext.doPath),
			"read":                               eval.NewBuiltin("module_ctx.read", moduleContext.doRead),
			"report_progress":                    eval.NewBuiltin("module_ctx.report_progress", moduleContext.doReportProgress),
			"root_module_has_non_dev_dependency": eval.Bool(usedModuleExtension.RootModuleHasNonDevDependency),
			"watch":                              eval.NewBuiltin("module_ctx.watch", moduleContext.doWatch),
			"which":                              eval.NewBuiltin("module_ctx.which", moduleContext.doWhich),

			// Fields specific to module_ctx.
			"extension_metadata": eval.NewBuiltin(
				"module_ctx.extension_metadata",
				func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
					// TODO: Properly implement this function.
					return eval.None, nil
				},
			),
			"is_dev_dependency": eval.NewBuiltin(
				"module_ctx.is_dev_dependency",
				func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
					var tag bazelModuleTag
					if err := eval.UnpackArgs(
						thread, b.Name(), args,
						"tag", unpack.Bind(&tag, unpack.Type[bazelModuleTag]("bazel_module_tag")),
					); err != nil {
						return nil, err
					}
					return eval.Bool(tag.isDevDependency), nil
				},
			),
			"modules": eval.NewImmutableList(modules),
		})

		// TODO: Capture extension_metadata.
		_, err = eval.Call(thread, model_starlark.DecodeFunction[TReference, TMetadata](model_core.Nested(moduleExtensionDefinitionValue, moduleExtensionDefinition.Implementation)), eval.Args{Positional: /* args = */ []eval.Value{moduleCtx}, Named: /* kwargs = */ nil})
		if err != nil {
			return PatchedModuleExtensionReposValue[TMetadata]{}, formatStarlarkError(err)
		}

		repos = repoRegistrar.GetRepos()
	}

	for _, name := range slices.Sorted(maps.Keys(repos)) {
		repo := repos[name]
		if err := treeBuilder.PushChild(model_core.NewPatchedMessage(
			&model_analysis_pb.ModuleExtensionRepos_Value_Repo{
				Level: &model_analysis_pb.ModuleExtensionRepos_Value_Repo_Leaf{
					Leaf: repo.Message,
				},
			},
			repo.Patcher,
		)); err != nil {
			return PatchedModuleExtensionReposValue[TMetadata]{}, err
		}
	}

	reposList, err := treeBuilder.FinalizeList()
	if err != nil {
		return PatchedModuleExtensionReposValue[TMetadata]{}, err
	}

	return model_core.NewPatchedMessage(
		&model_analysis_pb.ModuleExtensionRepos_Value{
			Repos: reposList.Message,
		},
		reposList.Patcher,
	), nil
}
