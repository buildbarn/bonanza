package analysis

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"bonanza.build/pkg/glob"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
)

// labelAttrRequirements contains the requirements that targets
// referenced by a label attr need to meet, as specified through the
// "providers", "allow_files" and "allow_single_file" parameters of
// attr.label() and friends.
type labelAttrRequirements[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	context      context.Context
	thread       *eval.Thread
	valueReaders *model_starlark.ValueReaders[TReference]

	requiredProviders []*model_starlark_pb.ProviderSet

	allowFileTargets       bool
	filenamePattern        *glob.NFA
	filenamePatternMatcher glob.Matcher
	allowSingleFile        bool
}

// newLabelAttrRequirements extracts the requirements that targets
// referenced by a label attr need to meet from the attr's definition.
func newLabelAttrRequirements[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	thread *eval.Thread,
	valueReaders *model_starlark.ValueReaders[TReference],
	attr *model_starlark_pb.Attr,
) (*labelAttrRequirements[TReference, TMetadata], error) {
	labelOptions, _, _ := getLabelAttrOptions(attr)
	r := &labelAttrRequirements[TReference, TMetadata]{
		context:      ctx,
		thread:       thread,
		valueReaders: valueReaders,

		requiredProviders: labelOptions.GetProviders(),
	}
	if allowFiles := labelOptions.GetAllowFiles(); allowFiles != nil {
		r.allowFileTargets = true
		if pattern := allowFiles.GetFilenamePattern(); len(pattern) > 0 {
			filenamePattern, err := glob.NewNFAFromBytes(pattern)
			if err != nil {
				return nil, fmt.Errorf("invalid allow_files filename pattern: %w", err)
			}
			r.filenamePattern = filenamePattern
		}
	}
	if attrType := attr.GetLabel(); attrType != nil {
		r.allowSingleFile = attrType.GetAllowSingleFile()
	}
	return r, nil
}

// matchesFilenamePattern returns whether a package relative path of a
// file matches the filename pattern provided to allow_files.
func (r *labelAttrRequirements[TReference, TMetadata]) matchesFilenamePattern(packageRelativePath string) bool {
	m := &r.filenamePatternMatcher
	m.Initialize(r.filenamePattern)
	for _, c := range packageRelativePath {
		if !m.WriteRune(c) {
			return false
		}
	}
	return m.IsMatch()
}

// check whether a target referenced by a label attr meets all of the
// requirements of the attr.
func (r *labelAttrRequirements[TReference, TMetadata]) check(
	resolvedLabel string,
	targetProviders model_core.Message[*model_analysis_pb.TargetProviders_Value, TReference],
) error {
	providerInstances := model_core.Nested(targetProviders, targetProviders.Message.ProviderInstances)
	if targetProviders.Message.IsFileTarget {
		// Like Bazel, file targets are only permitted if
		// allow_files is set. They are not checked against
		// the required providers.
		if !r.allowFileTargets {
			return fmt.Errorf("target %#v is a file target, which is not permitted as allow_files is not set", resolvedLabel)
		}
	} else if err := checkRequiredProviders(resolvedLabel, providerInstances.Message, r.requiredProviders); err != nil {
		return err
	}
	return r.checkFiles(resolvedLabel, providerInstances)
}

// checkFiles checks whether the files yielded by a target referenced by
// a label attr are permitted by the attr's allow_files and
// allow_single_file parameters. Like Bazel, DefaultInfo.files needs to
// contain at least one file matching the filename pattern provided to
// allow_files, or a directory. Files that don't match are permitted. As
// the DefaultInfo provider of file targets only contains the file
// itself, this effectively matches the name of file targets.
func (r *labelAttrRequirements[TReference, TMetadata]) checkFiles(
	resolvedLabel string,
	providerInstances model_core.Message[[]*model_starlark_pb.Struct, TReference],
) error {
	if r.filenamePattern == nil && !r.allowSingleFile {
		return nil
	}

	_, filesDepset, err := getDefaultInfoFilesDepset(r.context, r.valueReaders, resolvedLabel, providerInstances)
	if err != nil {
		return err
	}
	depsetElements := filesDepset.Message.GetElements()
	filesElements := make([]any, 0, len(depsetElements))
	for _, element := range depsetElements {
		filesElements = append(filesElements, model_core.Nested(filesDepset, element))
	}
	files, err := model_starlark.NewDepsetContentsFromList[TReference, TMetadata](
		filesElements,
		model_starlark_pb.Depset_DEFAULT,
	).ToList(r.thread)
	if err != nil {
		return fmt.Errorf("converting files of target with label %#v to list: %w", resolvedLabel, err)
	}

	if r.allowSingleFile && len(files) != 1 {
		return fmt.Errorf("target %#v must produce a single file, but it produces %d files", resolvedLabel, len(files))
	}
	if r.filenamePattern == nil {
		return nil
	}
	for _, file := range files {
		typedFile, ok := file.(*model_starlark.File[TReference, TMetadata])
		if !ok {
			return fmt.Errorf("files of target with label %#v contain a value of type %s, while File was expected", resolvedLabel, file.Type())
		}
		fileDefinition := typedFile.GetDefinition().Message
		if fileDefinition.GetOwner().GetType() == model_starlark_pb.File_Owner_DIRECTORY {
			// Like Bazel, accept directories, as their
			// contents are unknown at this point.
			return nil
		}
		fileLabel, err := label.NewCanonicalLabel(fileDefinition.GetLabel())
		if err != nil {
			return fmt.Errorf("invalid label %#v of file of target with label %#v: %w", fileDefinition.GetLabel(), resolvedLabel, err)
		}
		if r.matchesFilenamePattern(fileLabel.GetTargetName().String()) {
			return nil
		}
	}
	return fmt.Errorf("target %#v does not produce any files permitted by allow_files", resolvedLabel)
}

// getDefaultInfoFilesDepset returns the fields of the DefaultInfo
// provider instance of a target, and the value of its "files" field.
func getDefaultInfoFilesDepset[TReference any](
	ctx context.Context,
	valueReaders *model_starlark.ValueReaders[TReference],
	resolvedLabel string,
	providerInstances model_core.Message[[]*model_starlark_pb.Struct, TReference],
) (model_core.Message[*model_starlark_pb.Struct_Fields, TReference], model_core.Message[*model_starlark_pb.Depset, TReference], error) {
	defaultInfoProviderIdentifierStr := defaultInfoProviderIdentifier.String()
	defaultInfoIndex, ok := sort.Find(
		len(providerInstances.Message),
		func(i int) int {
			return strings.Compare(defaultInfoProviderIdentifierStr, providerInstances.Message[i].GetProviderInstanceProperties().GetProviderIdentifier())
		},
	)
	if !ok {
		return model_core.Message[*model_starlark_pb.Struct_Fields, TReference]{}, model_core.Message[*model_starlark_pb.Depset, TReference]{}, fmt.Errorf("target with label %#v did not yield provider %#v", resolvedLabel, defaultInfoProviderIdentifierStr)
	}
	defaultInfoFields := model_core.Nested(providerInstances, providerInstances.Message[defaultInfoIndex].GetFields())
	files, err := model_starlark.GetStructFieldValue(ctx, valueReaders.List, defaultInfoFields, "files")
	if err != nil {
		return model_core.Message[*model_starlark_pb.Struct_Fields, TReference]{}, model_core.Message[*model_starlark_pb.Depset, TReference]{}, fmt.Errorf("failed to obtain field \"files\" of DefaultInfo provider of target with label %#v: %w", resolvedLabel, err)
	}
	valueDepset := files.Message.GetDepset()
	if valueDepset == nil {
		return model_core.Message[*model_starlark_pb.Struct_Fields, TReference]{}, model_core.Message[*model_starlark_pb.Depset, TReference]{}, fmt.Errorf("field \"files\" of DefaultInfo provider of target with label %#v is not a depset", resolvedLabel)
	}
	return defaultInfoFields, model_core.Nested(files, valueDepset), nil
}
