package analysis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
)

type getPackageDirectoryEnvironment[TReference any] interface {
	GetRepoValue(*model_analysis_pb.Repo_Key) model_core.Message[*model_analysis_pb.Repo_Value, TReference]
}

func (baseComputer[TReference, TMetadata]) getPackageDirectory(
	ctx context.Context,
	e getPackageDirectoryEnvironment[TReference],
	directoryContentsReader model_parser.ParsedObjectReader[model_core.Decodable[TReference], model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]],
	canonicalPackageStr string,
) (model_core.Message[*model_filesystem_pb.DirectoryContents, TReference], error) {
	canonicalPackage, err := label.NewCanonicalPackage(canonicalPackageStr)
	if err != nil {
		return model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]{}, errors.New("invalid base package")
	}

	repoValue := e.GetRepoValue(&model_analysis_pb.Repo_Key{
		CanonicalRepo: canonicalPackage.GetCanonicalRepo().String(),
	})
	if !repoValue.IsSet() {
		return model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]{}, evaluation.ErrMissingDependency
	}

	// Obtain the root directory of the repo.
	baseDirectory, err := model_parser.Dereference(ctx, directoryContentsReader, model_core.Nested(repoValue, repoValue.Message.RootDirectoryReference.GetReference()))
	if err != nil {
		return model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]{}, err
	}

	// Traverse into the directory belonging to the package path.
	for component := range strings.FieldsFuncSeq(
		canonicalPackage.GetPackagePath(),
		func(r rune) bool { return r == '/' },
	) {
		directories := baseDirectory.Message.Directories
		directoryIndex, ok := sort.Find(
			len(directories),
			func(i int) int { return strings.Compare(component, directories[i].Name) },
		)
		if !ok {
			// Base package does not exist.
			return model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]{}, nil
		}
		baseDirectory, err = model_filesystem.DirectoryGetContents(
			ctx,
			directoryContentsReader,
			model_core.Nested(baseDirectory, directories[directoryIndex].Directory),
		)
		if err != nil {
			return model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]{}, err
		}
	}
	return baseDirectory, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputePackagesAtAndBelowValue(ctx context.Context, key *model_analysis_pb.PackagesAtAndBelow_Key, e PackagesAtAndBelowEnvironment[TReference, TMetadata]) (PatchedPackagesAtAndBelowValue[TMetadata], error) {
	directoryReaders, gotDirectoryReaders := e.GetDirectoryReadersValue(&model_analysis_pb.DirectoryReaders_Key{})
	if !gotDirectoryReaders {
		return PatchedPackagesAtAndBelowValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	packageDirectory, err := c.getPackageDirectory(ctx, e, directoryReaders.DirectoryContents, key.BasePackage)
	if err != nil {
		return PatchedPackagesAtAndBelowValue[TMetadata]{}, err
	}
	if !packageDirectory.IsSet() {
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.PackagesAtAndBelow_Value{}), nil
	}

	// Find packages at and below the base package.
	checker := packageExistenceChecker[TReference]{
		context:          ctx,
		directoryReaders: directoryReaders,
	}
	packageAtBasePackage, err := directoryIsPackage(ctx, directoryReaders.Leaves, packageDirectory)
	if err != nil {
		return PatchedPackagesAtAndBelowValue[TMetadata]{}, err
	}
	if err := checker.findPackagesBelow(packageDirectory, nil); err != nil {
		return PatchedPackagesAtAndBelowValue[TMetadata]{}, err
	}

	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.PackagesAtAndBelow_Value{
		PackageAtBasePackage:     packageAtBasePackage,
		PackagesBelowBasePackage: checker.packagesBelowBasePackage,
	}), nil
}

type packageExistenceChecker[TReference any] struct {
	context                  context.Context
	directoryReaders         *DirectoryReaders[TReference]
	packagesBelowBasePackage []string
}

func (pec *packageExistenceChecker[TReference]) findPackagesBelow(d model_core.Message[*model_filesystem_pb.DirectoryContents, TReference], dTrace *path.Trace) error {
	for _, entry := range d.Message.Directories {
		name, ok := path.NewComponent(entry.Name)
		if !ok {
			return fmt.Errorf("invalid directory name %#v in directory %#v", entry.Name, dTrace.GetUNIXString())
		}
		childTrace := dTrace.Append(name)

		childDirectory, err := model_filesystem.DirectoryGetContents(
			pec.context,
			pec.directoryReaders.DirectoryContents,
			model_core.Nested(d, entry.Directory),
		)
		if err != nil {
			return fmt.Errorf("failed to get contents of directory %#v: %w", childTrace.GetUNIXString(), err)
		}

		directoryIsPackage, err := directoryIsPackage(pec.context, pec.directoryReaders.Leaves, childDirectory)
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

// directoryPackage returns true if the provided directory is the root
// directory of a package. A directory is a root directory of a package
// if it contains a BUILD.bazel or BUILD file.
func directoryIsPackage[TReference any](ctx context.Context, leavesReader model_parser.ParsedObjectReader[model_core.Decodable[TReference], model_core.Message[*model_filesystem_pb.Leaves, TReference]], d model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]) (bool, error) {
	leaves, err := model_filesystem.DirectoryGetLeaves(ctx, leavesReader, d)
	if err != nil {
		return false, err
	}

	// TODO: Should we also consider symlinks having such names?
	files := leaves.Message.Files
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
