package analysis

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	pg_starlark "bonanza.build/pkg/starlark"
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/starlark"
)

type usedModuleExtension struct {
	message model_analysis_pb.ModuleExtension
	users   map[label.ModuleInstance]*moduleExtensionUser
}

type moduleExtensionUser struct {
	message    model_analysis_pb.ModuleExtension_User
	tagClasses map[string]*model_analysis_pb.ModuleExtension_TagClass
}

type usedModuleExtensionOptions[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	labelResolver label.Resolver
	patcher       *model_core.ReferenceMessagePatcher[TMetadata]
}

type usedModuleExtensionProxy[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	handler       *usedModuleExtensionExtractingModuleDotBazelHandler[TReference, TMetadata]
	user          *moduleExtensionUser
	devDependency bool
}

func (p *usedModuleExtensionProxy[TReference, TMetadata]) Tag(className string, attrs map[string]starlark.Value) error {
	meu := p.user
	tagClass, ok := meu.tagClasses[className]
	if !ok {
		tagClass = &model_analysis_pb.ModuleExtension_TagClass{
			Name: className,
		}
		meu.tagClasses[className] = tagClass
	}

	attrsMap := make(map[string]any, len(attrs))
	for key, value := range attrs {
		attrsMap[key] = value
	}
	fields, _, err := model_starlark.NewStructFromDict[TReference, TMetadata](nil, attrsMap).
		EncodeStructFields(map[starlark.Value]struct{}{}, p.handler.valueEncodingOptions)
	if err != nil {
		return err
	}
	p.handler.options.patcher.Merge(fields.Patcher)

	tagClass.Tags = append(tagClass.Tags, &model_analysis_pb.ModuleExtension_Tag{
		IsDevDependency: p.devDependency,
		Attrs:           fields.Message,
	})
	return nil
}

func (usedModuleExtensionProxy[TReference, TMetadata]) UseRepo(repos map[label.ApparentRepo]label.ApparentRepo) error {
	return nil
}

type usedModuleExtensionExtractingModuleDotBazelHandler[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	options               *usedModuleExtensionOptions[TReference, TMetadata]
	moduleInstance        label.ModuleInstance
	isRoot                bool
	ignoreDevDependencies bool
	usedModuleExtensions  map[label.ModuleExtension]*usedModuleExtension
	valueEncodingOptions  *model_starlark.ValueEncodingOptions[TReference, TMetadata]
}

func (usedModuleExtensionExtractingModuleDotBazelHandler[TReference, TMetadata]) BazelDep(name label.Module, version *label.ModuleVersion, maxCompatibilityLevel int, repoName label.ApparentRepo, devDependency bool) error {
	return nil
}

func (usedModuleExtensionExtractingModuleDotBazelHandler[TReference, TMetadata]) Module(name label.Module, version *label.ModuleVersion, compatibilityLevel int, repoName label.ApparentRepo, bazelCompatibility []string) error {
	return nil
}

func (usedModuleExtensionExtractingModuleDotBazelHandler[TReference, TMetadata]) RegisterExecutionPlatforms(platformTargetPatterns []label.ApparentTargetPattern, devDependency bool) error {
	return nil
}

func (usedModuleExtensionExtractingModuleDotBazelHandler[TReference, TMetadata]) RegisterToolchains(toolchainTargetPatterns []label.ApparentTargetPattern, devDependency bool) error {
	return nil
}

func (h *usedModuleExtensionExtractingModuleDotBazelHandler[TReference, TMetadata]) UseExtension(extensionBzlFile label.ApparentLabel, extensionName label.StarlarkIdentifier, devDependency, isolate bool) (pg_starlark.ModuleExtensionProxy, error) {
	if devDependency && h.ignoreDevDependencies {
		return pg_starlark.NullModuleExtensionProxy, nil
	}

	// Look up the module extension properties, so that we obtain
	// the canonical identifier of the Starlark module_extension
	// declaration.
	canonicalExtensionBzlFile, err := label.Canonicalize(h.options.labelResolver, h.moduleInstance.GetBareCanonicalRepo(), extensionBzlFile)
	if err != nil {
		return nil, err
	}
	moduleExtensionIdentifier := canonicalExtensionBzlFile.AppendStarlarkIdentifier(extensionName)
	moduleExtensionName := moduleExtensionIdentifier.ToModuleExtension()
	moduleExtensionIdentifierStr := moduleExtensionIdentifier.String()

	ume, ok := h.usedModuleExtensions[moduleExtensionName]
	if ok {
		// Safety belt: prevent a single module instance from
		// declaring multiple module extensions with the same
		// name. This would cause collisions when both of them
		// declare repos with the same name.
		if moduleExtensionIdentifierStr != ume.message.Identifier {
			return nil, fmt.Errorf(
				"module extension declarations %#v and %#v have the same name, meaning they would both declare repos with prefix %#v",
				moduleExtensionIdentifierStr,
				ume.message.Identifier,
				moduleExtensionName.String(),
			)
		}
	} else {
		ume = &usedModuleExtension{
			message: model_analysis_pb.ModuleExtension{
				Identifier: moduleExtensionIdentifierStr,
			},
			users: map[label.ModuleInstance]*moduleExtensionUser{},
		}
		h.usedModuleExtensions[moduleExtensionName] = ume
	}

	meu, ok := ume.users[h.moduleInstance]
	if !ok {
		meu = &moduleExtensionUser{
			message: model_analysis_pb.ModuleExtension_User{
				ModuleInstance: h.moduleInstance.String(),
				IsRoot:         h.isRoot,
			},
			tagClasses: map[string]*model_analysis_pb.ModuleExtension_TagClass{},
		}
		ume.users[h.moduleInstance] = meu
		ume.message.Users = append(ume.message.Users, &meu.message)
	}

	return &usedModuleExtensionProxy[TReference, TMetadata]{
		handler:       h,
		user:          meu,
		devDependency: devDependency,
	}, nil
}

func (usedModuleExtensionExtractingModuleDotBazelHandler[TReference, TMetadata]) UseRepoRule(repoRuleBzlFile label.ApparentLabel, repoRuleName label.StarlarkIdentifier) (pg_starlark.RepoRuleProxy, error) {
	return func(name label.ApparentRepo, devDependency bool, attrs map[string]starlark.Value) error {
		return nil
	}, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeUsedModuleExtensionsValue(ctx context.Context, key *model_analysis_pb.UsedModuleExtensions_Key, e UsedModuleExtensionsEnvironment[TReference, TMetadata]) (PatchedUsedModuleExtensionsValue[TMetadata], error) {
	options := usedModuleExtensionOptions[TReference, TMetadata]{
		labelResolver: newLabelResolver(e),
		patcher:       model_core.NewReferenceMessagePatcher[TMetadata](),
	}
	usedModuleExtensions := map[label.ModuleExtension]*usedModuleExtension{}
	isRoot := true
	if err := c.visitModuleDotBazelFilesBreadthFirst(ctx, e, func(moduleInstance label.ModuleInstance, ignoreDevDependencies bool) pg_starlark.ChildModuleDotBazelHandler {
		h := &usedModuleExtensionExtractingModuleDotBazelHandler[TReference, TMetadata]{
			options:               &options,
			moduleInstance:        moduleInstance,
			isRoot:                isRoot,
			ignoreDevDependencies: ignoreDevDependencies,
			usedModuleExtensions:  usedModuleExtensions,
			valueEncodingOptions:  c.getValueEncodingOptions(ctx, e, nil),
		}
		isRoot = false
		return h
	}); err != nil {
		return PatchedUsedModuleExtensionsValue[TMetadata]{}, err
	}

	// Sort and populate tag classes of each module extension user.
	for _, ume := range usedModuleExtensions {
		for _, meu := range ume.users {
			meu.message.TagClasses = make([]*model_analysis_pb.ModuleExtension_TagClass, 0, len(meu.tagClasses))
			for _, name := range slices.Sorted(maps.Keys(meu.tagClasses)) {
				meu.message.TagClasses = append(meu.message.TagClasses, meu.tagClasses[name])
			}
		}
	}

	sortedModuleExtensions := make([]*model_analysis_pb.ModuleExtension, 0, len(usedModuleExtensions))
	for _, name := range slices.SortedFunc(
		maps.Keys(usedModuleExtensions),
		func(a, b label.ModuleExtension) int { return strings.Compare(a.String(), b.String()) },
	) {
		sortedModuleExtensions = append(sortedModuleExtensions, &usedModuleExtensions[name].message)
	}
	return model_core.NewPatchedMessage(
		&model_analysis_pb.UsedModuleExtensions_Value{
			ModuleExtensions: sortedModuleExtensions,
		},
		options.patcher,
	), nil
}
