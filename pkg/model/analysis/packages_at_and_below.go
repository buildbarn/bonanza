package analysis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bonanza/pkg/evaluation"
	"github.com/buildbarn/bonanza/pkg/label"
	model_core "github.com/buildbarn/bonanza/pkg/model/core"
	model_parser "github.com/buildbarn/bonanza/pkg/model/parser"
	model_analysis_pb "github.com/buildbarn/bonanza/pkg/proto/model/analysis"
	model_filesystem_pb "github.com/buildbarn/bonanza/pkg/proto/model/filesystem"
	"github.com/buildbarn/bonanza/pkg/storage/dag"
	"github.com/buildbarn/bonanza/pkg/storage/object"
)

func (c *baseComputer) ComputePackagesAtAndBelowValue(ctx context.Context, key *model_analysis_pb.PackagesAtAndBelow_Key, e PackagesAtAndBelowEnvironment) (PatchedPackagesAtAndBelowValue, error) {
	basePackage, err := label.NewCanonicalPackage(key.BasePackage)
	if err != nil {
		return PatchedPackagesAtAndBelowValue{}, errors.New("invalid base package")
	}

	directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObjectValue(&model_analysis_pb.DirectoryCreationParametersObject_Key{})
	repoValue := e.GetRepoValue(&model_analysis_pb.Repo_Key{
		CanonicalRepo: basePackage.GetCanonicalRepo().String(),
	})
	if !gotDirectoryCreationParameters || !repoValue.IsSet() {
		return PatchedPackagesAtAndBelowValue{}, evaluation.ErrMissingDependency
	}

	// Obtain the root directory of the repo.
	rootDirectoryReference, err := repoValue.GetOutgoingReference(repoValue.Message.RootDirectoryReference.GetReference())
	if err != nil {
		return PatchedPackagesAtAndBelowValue{}, fmt.Errorf("invalid root directory reference: %w", err)
	}
	directoryReader := model_parser.NewStorageBackedParsedObjectReader(
		c.objectDownloader,
		directoryCreationParameters.GetEncoder(),
		model_parser.NewMessageObjectParser[object.LocalReference, model_filesystem_pb.Directory](),
	)
	baseDirectory, _, err := directoryReader.ReadParsedObject(ctx, rootDirectoryReference)
	if err != nil {
		return PatchedPackagesAtAndBelowValue{}, err
	}

	// Traverse into the directory belonging to the package path.
	for component := range strings.FieldsFuncSeq(
		basePackage.GetPackagePath(),
		func(r rune) bool { return r == '/' },
	) {
		directories := baseDirectory.Message.Directories
		directoryIndex, ok := sort.Find(
			len(directories),
			func(i int) int { return strings.Compare(component, directories[i].Name) },
		)
		if !ok {
			// Base package does not exist.
			return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](&model_analysis_pb.PackagesAtAndBelow_Value{}), nil
		}
		switch contents := directories[directoryIndex].Contents.(type) {
		case *model_filesystem_pb.DirectoryNode_ContentsExternal:
			directoryReference, err := baseDirectory.GetOutgoingReference(contents.ContentsExternal.Reference)
			if err != nil {
				return PatchedPackagesAtAndBelowValue{}, err
			}
			baseDirectory, _, err = directoryReader.ReadParsedObject(ctx, directoryReference)
			if err != nil {
				return PatchedPackagesAtAndBelowValue{}, err
			}
		case *model_filesystem_pb.DirectoryNode_ContentsInline:
			baseDirectory = model_core.NewNestedMessage(baseDirectory, contents.ContentsInline)
		default:
			return PatchedPackagesAtAndBelowValue{}, errors.New("invalid directory contents type")
		}
	}

	// Find packages at and below the base package.
	checker := packageExistenceChecker{
		context:         ctx,
		directoryReader: directoryReader,
		leavesReader: model_parser.NewStorageBackedParsedObjectReader(
			c.objectDownloader,
			directoryCreationParameters.GetEncoder(),
			model_parser.NewMessageObjectParser[object.LocalReference, model_filesystem_pb.Leaves](),
		),
	}
	packageAtBasePackage, err := checker.directoryIsPackage(baseDirectory)
	if err != nil {
		return PatchedPackagesAtAndBelowValue{}, err
	}
	if err := checker.findPackagesBelow(baseDirectory, nil); err != nil {
		return PatchedPackagesAtAndBelowValue{}, err
	}

	return model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](&model_analysis_pb.PackagesAtAndBelow_Value{
		PackageAtBasePackage:     packageAtBasePackage,
		PackagesBelowBasePackage: checker.packagesBelowBasePackage,
	}), nil
}

type packageExistenceChecker struct {
	context                  context.Context
	directoryReader          model_parser.ParsedObjectReader[object.LocalReference, model_core.Message[*model_filesystem_pb.Directory]]
	leavesReader             model_parser.ParsedObjectReader[object.LocalReference, model_core.Message[*model_filesystem_pb.Leaves]]
	packagesBelowBasePackage []string
}

func (pec *packageExistenceChecker) directoryIsPackage(d model_core.Message[*model_filesystem_pb.Directory]) (bool, error) {
	var leaves *model_filesystem_pb.Leaves
	switch l := d.Message.Leaves.(type) {
	case *model_filesystem_pb.Directory_LeavesExternal:
		leavesReference, err := d.GetOutgoingReference(l.LeavesExternal.Reference)
		if err != nil {
			return false, err
		}
		externalLeaves, _, err := pec.leavesReader.ReadParsedObject(pec.context, leavesReference)
		if err != nil {
			return false, err
		}
		leaves = externalLeaves.Message
	case *model_filesystem_pb.Directory_LeavesInline:
		leaves = l.LeavesInline
	default:
		return false, errors.New("invalid leaves type")
	}

	// TODO: Should we also consider symlinks having such names?
	files := leaves.Files
	for _, filename := range buildDotBazelTargetNames {
		filenameStr := filename.String()
		if _, ok := sort.Find(
			len(files),
			func(i int) int { return strings.Compare(filenameStr, files[i].Name) },
		); ok {
			// Current directory is a package.
			return true, nil
		}
	}
	return false, nil
}

func (pec *packageExistenceChecker) findPackagesBelow(d model_core.Message[*model_filesystem_pb.Directory], dTrace *path.Trace) error {
	for _, entry := range d.Message.Directories {
		name, ok := path.NewComponent(entry.Name)
		if !ok {
			return fmt.Errorf("invalid directory name %#v in directory %#v", entry.Name, dTrace.GetUNIXString())
		}
		childTrace := dTrace.Append(name)

		var childDirectory model_core.Message[*model_filesystem_pb.Directory]
		switch contents := entry.Contents.(type) {
		case *model_filesystem_pb.DirectoryNode_ContentsExternal:
			directoryReference, err := d.GetOutgoingReference(contents.ContentsExternal.Reference)
			if err != nil {
				return err
			}
			childDirectory, _, err = pec.directoryReader.ReadParsedObject(pec.context, directoryReference)
			if err != nil {
				return err
			}
		case *model_filesystem_pb.DirectoryNode_ContentsInline:
			childDirectory = model_core.NewNestedMessage(d, contents.ContentsInline)
		default:
			return fmt.Errorf("invalid contents type for directory %#v", childTrace.GetUNIXString())
		}

		directoryIsPackage, err := pec.directoryIsPackage(childDirectory)
		if err != nil {
			return fmt.Errorf("failed to determine whether directory %#v is a package: %w", dTrace.GetUNIXString(), err)
		}
		if directoryIsPackage {
			pec.packagesBelowBasePackage = append(pec.packagesBelowBasePackage, childTrace.GetUNIXString())
		} else {
			// Not a package. Find packages below.
			if err := pec.findPackagesBelow(childDirectory, childTrace); err != nil {
				return err
			}
		}
	}
	return nil
}
