package analysis

import (
	"context"
	"fmt"

	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
)

// labelAttrFiles collects the values of ctx.executable, ctx.file and
// ctx.files for the label attrs of a rule or aspect, based on the
// DefaultInfo providers of the targets referenced by the attrs.
type labelAttrFiles[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	context      context.Context
	thread       *eval.Thread
	valueReaders *model_starlark.ValueReaders[TReference]

	executableValues           map[string]any
	executableFileToFilesToRun map[*model_starlark.File[TReference, TMetadata]]model_core.Message[*model_starlark_pb.Struct, TReference]
	fileValues                 map[string]any
	filesValues                map[string]any
}

func newLabelAttrFiles[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	thread *eval.Thread,
	valueReaders *model_starlark.ValueReaders[TReference],
) *labelAttrFiles[TReference, TMetadata] {
	return &labelAttrFiles[TReference, TMetadata]{
		context:      ctx,
		thread:       thread,
		valueReaders: valueReaders,

		executableValues:           map[string]any{},
		executableFileToFilesToRun: map[*model_starlark.File[TReference, TMetadata]]model_core.Message[*model_starlark_pb.Struct, TReference]{},
		fileValues:                 map[string]any{},
		filesValues:                map[string]any{},
	}
}

// newAttr returns a collector for the files of a single label attr.
func (f *labelAttrFiles[TReference, TMetadata]) newAttr(namedAttr *model_starlark_pb.NamedAttr) *labelAttrFilesCollector[TReference, TMetadata] {
	c := &labelAttrFilesCollector[TReference, TMetadata]{
		files:    f,
		attrName: namedAttr.Name,
	}
	if attrType, ok := namedAttr.Attr.GetType().(*model_starlark_pb.Attr_Label); ok {
		c.allowSingleFile = attrType.Label.AllowSingleFile
		c.executable = attrType.Label.Executable
	}
	return c
}

// labelAttrFilesCollector collects the files of the targets referenced
// by a single label attr.
type labelAttrFilesCollector[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	files           *labelAttrFiles[TReference, TMetadata]
	attrName        string
	allowSingleFile bool
	executable      bool

	filesDepsetElements []any
}

// setConfigured indicates that targets referenced by the attr are
// configured. This causes ctx.executable to be set to None for
// executable attrs, even if no targets are referenced.
func (c *labelAttrFilesCollector[TReference, TMetadata]) setConfigured() {
	if c.executable {
		c.files.executableValues[c.attrName] = eval.None
	}
}

// addTarget extracts the files and executable from the DefaultInfo
// provider of a target that is referenced by the label attr.
func (c *labelAttrFilesCollector[TReference, TMetadata]) addTarget(
	resolvedLabel string,
	providerInstances model_core.Message[[]*model_starlark_pb.Struct, TReference],
) error {
	f := c.files
	ctx := f.context
	valueReaders := f.valueReaders
	defaultInfoFields, filesDepset, err := getDefaultInfoFilesDepset(ctx, valueReaders, resolvedLabel, providerInstances)
	if err != nil {
		return err
	}
	for _, element := range filesDepset.Message.Elements {
		c.filesDepsetElements = append(c.filesDepsetElements, model_core.Nested(filesDepset, element))
	}

	if c.executable {
		filesToRun, err := model_starlark.GetStructFieldValue(ctx, valueReaders.List, defaultInfoFields, "files_to_run")
		if err != nil {
			return fmt.Errorf("failed to obtain field \"files_to_run\" of DefaultInfo provider of target with label %#v: %w", resolvedLabel, err)
		}
		filesToRunStructValue, ok := filesToRun.Message.Kind.(*model_starlark_pb.Value_Struct)
		if !ok {
			return fmt.Errorf("field \"files_to_run\" of DefaultInfo provider of target with label %#v is not a struct", resolvedLabel)
		}
		filesToRunStruct := model_core.Nested(filesToRun, filesToRunStructValue.Struct)
		executableField, err := model_starlark.GetStructFieldValue(
			ctx,
			valueReaders.List,
			model_core.Nested(filesToRunStruct, filesToRunStruct.Message.Fields),
			"executable",
		)
		if err != nil {
			return fmt.Errorf("failed to obtain field \"files_to_run.executable\" of DefaultInfo provider of target with label %#v: %w", resolvedLabel, err)
		}
		decodedExecutable, err := model_starlark.DecodeValue[TReference, TMetadata](f.thread, executableField, nil)
		if err != nil {
			return fmt.Errorf("decode field \"files_to_run.executable\" of DefaultInfo provider of target with label %#v: %w", resolvedLabel, err)
		}
		typedExecutable, ok := decodedExecutable.(*model_starlark.File[TReference, TMetadata])
		if !ok {
			return fmt.Errorf("field \"files_to_run.executable\" of DefaultInfo provider of target with label %#v is not a File", resolvedLabel)
		}
		f.executableValues[c.attrName] = typedExecutable
		f.executableFileToFilesToRun[typedExecutable] = filesToRunStruct
	}
	return nil
}

// finish computes the value of ctx.file or ctx.files for the label
// attr, after all targets referenced by it have been added.
func (c *labelAttrFilesCollector[TReference, TMetadata]) finish() error {
	f := c.files
	filesElements, err := model_starlark.NewDepsetContentsFromList[TReference, TMetadata](
		c.filesDepsetElements,
		model_starlark_pb.Depset_DEFAULT,
	).ToList(f.thread)
	if err != nil {
		return fmt.Errorf("converting files depset to list: %w", err)
	}
	if c.allowSingleFile {
		switch l := len(filesElements); l {
		case 0:
			f.fileValues[c.attrName] = eval.None
		case 1:
			f.fileValues[c.attrName] = filesElements[0]
		default:
			return fmt.Errorf("attr %#v has allow_single_file=True, but its value expands to %d targets", c.attrName, l)
		}
	} else {
		f.filesValues[c.attrName] = eval.NewImmutableList(filesElements)
	}
	return nil
}
