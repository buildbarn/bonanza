package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bonanza/pkg/evaluation"
	"github.com/buildbarn/bonanza/pkg/label"
	model_core "github.com/buildbarn/bonanza/pkg/model/core"
	"github.com/buildbarn/bonanza/pkg/model/core/btree"
	model_filesystem "github.com/buildbarn/bonanza/pkg/model/filesystem"
	model_starlark "github.com/buildbarn/bonanza/pkg/model/starlark"
	model_analysis_pb "github.com/buildbarn/bonanza/pkg/proto/model/analysis"
	model_core_pb "github.com/buildbarn/bonanza/pkg/proto/model/core"
	model_filesystem_pb "github.com/buildbarn/bonanza/pkg/proto/model/filesystem"
	model_starlark_pb "github.com/buildbarn/bonanza/pkg/proto/model/starlark"
	"github.com/buildbarn/bonanza/pkg/storage/dag"
	"github.com/buildbarn/bonanza/pkg/storage/object"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/encoding/protojson"
)

type getStarlarkFilePropertiesEnvironment[TReference any, TMetadata any] interface {
	model_core.ExistingObjectCapturer[TReference, TMetadata]

	GetFilePropertiesValue(key *model_analysis_pb.FileProperties_Key) model_core.Message[*model_analysis_pb.FileProperties_Value, TReference]
	GetTargetOutputValue(key model_core.PatchedMessage[*model_analysis_pb.TargetOutput_Key, dag.ObjectContentsWalker]) model_core.Message[*model_analysis_pb.TargetOutput_Value, TReference]
}

func getStarlarkFileProperties[TReference object.BasicReference, TMetadata model_core.WalkableReferenceMetadata](e getStarlarkFilePropertiesEnvironment[TReference, TMetadata], f model_core.Message[*model_starlark_pb.File, TReference]) (model_core.Message[*model_filesystem_pb.FileProperties, TReference], error) {
	if f.Message == nil {
		return model_core.Message[*model_filesystem_pb.FileProperties, TReference]{}, errors.New("file not set")
	}
	canonicalPackage, err := label.NewCanonicalPackage(f.Message.Package)
	if err != nil {
		return model_core.Message[*model_filesystem_pb.FileProperties, TReference]{}, fmt.Errorf("invalid package %#v: %w", f.Message.Package, err)
	}

	if owner := f.Message.Owner; owner != nil {
		// File is an output file. Build it.
		targetName, err := label.NewTargetName(owner.TargetName)
		if err != nil {
			return model_core.Message[*model_filesystem_pb.FileProperties, TReference]{}, fmt.Errorf("invalid target name %#v: %w", owner.TargetName, err)
		}

		configurationReference := model_core.Patch(e, model_core.Nested(f, owner.ConfigurationReference))
		targetOutput := e.GetTargetOutputValue(
			model_core.NewPatchedMessage(
				&model_analysis_pb.TargetOutput_Key{
					TargetLabel:            canonicalPackage.AppendTargetName(targetName).String(),
					PackageRelativePath:    f.Message.PackageRelativePath,
					ConfigurationReference: configurationReference.Message,
				},
				model_core.MapReferenceMetadataToWalkers(configurationReference.Patcher),
			),
		)
		if !targetOutput.IsSet() {
			return model_core.Message[*model_filesystem_pb.FileProperties, TReference]{}, evaluation.ErrMissingDependency
		}

		return model_core.Message[*model_filesystem_pb.FileProperties, TReference]{}, fmt.Errorf("TODO: PROCESS TARGET OUTPUT: %s", protojson.Format(targetOutput.Message))
	}

	// File is a source file. Fetch it from its repo.
	packageRelativePath, err := label.NewTargetName(f.Message.PackageRelativePath)
	if err != nil {
		return model_core.Message[*model_filesystem_pb.FileProperties, TReference]{}, fmt.Errorf("invalid package relative path %#v: %w", f.Message.PackageRelativePath, err)
	}
	fileProperties := e.GetFilePropertiesValue(
		&model_analysis_pb.FileProperties_Key{
			CanonicalRepo: canonicalPackage.GetCanonicalRepo().String(),
			Path:          canonicalPackage.AppendTargetName(packageRelativePath).GetRepoRelativePath(),
		},
	)
	if !fileProperties.IsSet() {
		return model_core.Message[*model_filesystem_pb.FileProperties, TReference]{}, evaluation.ErrMissingDependency
	}
	exists := fileProperties.Message.Exists
	if exists == nil {
		return model_core.Message[*model_filesystem_pb.FileProperties, TReference]{}, errors.New("source file does not exist")
	}
	return model_core.Nested(fileProperties, exists), nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeTargetOutputValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetOutput_Key, TReference], e TargetOutputEnvironment[TReference, TMetadata]) (PatchedTargetOutputValue, error) {
	configurationReference := model_core.Nested(key, key.Message.ConfigurationReference)
	patchedConfigurationReference := model_core.Patch(e, configurationReference)
	configuredTarget := e.GetConfiguredTargetValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.ConfiguredTarget_Key{
				Label:                  key.Message.TargetLabel,
				ConfigurationReference: patchedConfigurationReference.Message,
			},
			model_core.MapReferenceMetadataToWalkers(patchedConfigurationReference.Patcher),
		),
	)
	if !configuredTarget.IsSet() {
		return PatchedTargetOutputValue{}, evaluation.ErrMissingDependency
	}

	packageRelativePath := key.Message.PackageRelativePath
	output, err := btree.Find(
		ctx,
		c.configuredTargetOutputReader,
		model_core.Nested(configuredTarget, configuredTarget.Message.Outputs),
		func(entry *model_analysis_pb.ConfiguredTarget_Value_Output) (int, *model_core_pb.Reference) {
			switch level := entry.Level.(type) {
			case *model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_:
				return strings.Compare(packageRelativePath, level.Leaf.PackageRelativePath), nil
			case *model_analysis_pb.ConfiguredTarget_Value_Output_Parent_:
				return strings.Compare(packageRelativePath, level.Parent.FirstPackageRelativePath), level.Parent.Reference
			default:
				return 0, nil
			}
		},
	)
	if err != nil {
		return PatchedTargetOutputValue{}, err
	}
	if !output.IsSet() {
		return PatchedTargetOutputValue{}, errors.New("target does not yield an output with the provided name")
	}
	outputLeaf, ok := output.Message.Level.(*model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_)
	if !ok {
		return PatchedTargetOutputValue{}, errors.New("unknown output level type")
	}

	switch source := outputLeaf.Leaf.Source.(type) {
	case *model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_ActionId:
		return PatchedTargetOutputValue{}, errors.New("TODO: invoke action")
	case *model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_ExpandTemplate_:
		if _, err := getStarlarkFileProperties(e, model_core.Nested(output, source.ExpandTemplate.Template)); err != nil {
			return PatchedTargetOutputValue{}, fmt.Errorf("failed to file properties of template: %w", err)
		}
		return PatchedTargetOutputValue{}, errors.New("TODO: expand template")
	case *model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_StaticPackageDirectory:
		// Output file was already computed during configuration.
		// For example by calling ctx.actions.write() or
		// ctx.actions.symlink(target_path=...).
		//
		// Wrap the package directory to make it an input root.
		directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObjectValue(&model_analysis_pb.DirectoryCreationParametersObject_Key{})
		if !gotDirectoryCreationParameters {
			return PatchedTargetOutputValue{}, evaluation.ErrMissingDependency
		}

		configurationComponent, err := model_starlark.ConfigurationReferenceToComponent(configurationReference)
		if err != nil {
			return PatchedTargetOutputValue{}, err
		}
		targetLabel, err := label.NewCanonicalLabel(key.Message.TargetLabel)
		if err != nil {
			return PatchedTargetOutputValue{}, fmt.Errorf("invalid target label: %w", err)
		}
		components := []path.Component{
			model_starlark.ComponentBazelOut,
			path.MustNewComponent(configurationComponent),
			model_starlark.ComponentBin,
			model_starlark.ComponentExternal,
			path.MustNewComponent(targetLabel.GetCanonicalRepo().String()),
		}
		for packageComponent := range strings.FieldsFuncSeq(targetLabel.GetCanonicalPackage().GetPackagePath(), func(r rune) bool { return r == '/' }) {
			components = append(components, path.MustNewComponent(packageComponent))
		}

		var createdDirectory model_filesystem.CreatedDirectory[TMetadata]
		group, groupCtx := errgroup.WithContext(ctx)
		group.Go(func() error {
			return model_filesystem.CreateDirectoryMerkleTree(
				groupCtx,
				semaphore.NewWeighted(1),
				group,
				directoryCreationParameters,
				&pathPrependingDirectory[TMetadata, TMetadata]{
					components: components,
					directory:  model_core.Patch(e, model_core.Nested(output, source.StaticPackageDirectory)),
				},
				model_filesystem.NewSimpleDirectoryMerkleTreeCapturer(e),
				&createdDirectory,
			)
		})
		if err := group.Wait(); err != nil {
			return PatchedTargetOutputValue{}, err
		}

		return model_core.NewPatchedMessage(
			&model_analysis_pb.TargetOutput_Value{
				RootDirectory: createdDirectory.Message.Message,
			},
			model_core.MapReferenceMetadataToWalkers(createdDirectory.Message.Patcher),
		), nil
	case *model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_Symlink:
		return PatchedTargetOutputValue{}, errors.New("TODO: symlink")
	default:
		return PatchedTargetOutputValue{}, errors.New("unknown output source type")
	}
}

type pathPrependingDirectory[TDirectory, TFile model_core.ReferenceMetadata] struct {
	components []path.Component
	directory  model_core.PatchedMessage[*model_filesystem_pb.Directory, TDirectory]
}

func (pathPrependingDirectory[TDirectory, TFile]) Close() error {
	return nil
}

func (d *pathPrependingDirectory[TDirectory, TFile]) ReadDir() ([]filesystem.FileInfo, error) {
	return []filesystem.FileInfo{
		filesystem.NewFileInfo(d.components[0], filesystem.FileTypeDirectory, false),
	}, nil
}

func (pathPrependingDirectory[TDirectory, TFile]) Readlink(name path.Component) (path.Parser, error) {
	panic("path prepending directory never contains symlinks")
}

func (d *pathPrependingDirectory[TDirectory, TFile]) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[TDirectory], model_filesystem.CapturableDirectory[TDirectory, TFile], error) {
	if len(d.components) > 1 {
		return nil, &pathPrependingDirectory[TDirectory, TFile]{
			components: d.components[1:],
			directory:  d.directory,
		}, nil
	}
	createdDirectory, err := model_filesystem.NewCreatedDirectoryBare(d.directory)
	return createdDirectory, nil, err
}

func (pathPrependingDirectory[TDirectory, TFile]) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[TFile], error) {
	panic("path prepending directory never contains regular files")
}
