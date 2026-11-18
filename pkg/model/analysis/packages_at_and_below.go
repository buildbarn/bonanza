package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/glob"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"
)

// allPackagesPattern can be provided to PackagesAtAndBelow.Key.pattern
// to obtain all packages below the base package. This is used to
// expand recursive target patterns (e.g., "//foo/...").
var allPackagesPattern = util.Must(glob.NewNFAFromPatterns([]string{"**"}, nil)).Bytes()

func getPackageDirectory[TReference object.BasicReference](
	ctx context.Context,
	e RepoValueGetter[TReference],
	directoryEntriesReader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry],
	canonicalPackageStr string,
) (model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], error) {
	canonicalPackage, err := label.NewCanonicalPackage(canonicalPackageStr)
	if err != nil {
		return model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]{}, errors.New("invalid base package")
	}

	repoValue := e.GetRepoValue(&model_analysis_pb.Repo_Key{
		CanonicalRepo: canonicalPackage.GetCanonicalRepo().String(),
	})
	if !repoValue.IsSet() {
		return model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]{}, evaluation.ErrMissingDependency
	}

	// Obtain the root directory of the repo.
	baseDirectory, err := model_filesystem.DirectoryDereference(ctx, directoryEntriesReader, model_core.Nested(repoValue, repoValue.Message.RootDirectoryReference))
	if err != nil {
		return model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]{}, err
	}

	// Traverse into the directory belonging to the package path.
	for component := range strings.FieldsFuncSeq(
		canonicalPackage.GetPackagePath(),
		func(r rune) bool { return r == '/' },
	) {
		name, ok := path.NewComponent(component)
		if !ok {
			return model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]{}, errors.New("invalid base package")
		}
		baseDirectory, err = model_filesystem.DirectoryLookupChild(ctx, directoryEntriesReader, baseDirectory, name)
		if err != nil {
			return model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]{}, err
		}
		if !baseDirectory.IsSet() {
			// Base package does not exist.
			return model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]{}, nil
		}
	}
	return baseDirectory, nil
}

func (*baseComputer[TReference, TMetadata]) ComputePackagesAtAndBelowValue(ctx context.Context, key *model_analysis_pb.PackagesAtAndBelow_Key, e PackagesAtAndBelowEnvironment[TReference, TMetadata]) (PatchedPackagesAtAndBelowValue[TMetadata], error) {
	directoryReaders, gotDirectoryReaders := e.GetDirectoryAccessParametersObject(&model_analysis_pb.DirectoryAccessParameters_Key{})
	if !gotDirectoryReaders {
		return PatchedPackagesAtAndBelowValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	packageDirectory, err := getPackageDirectory(ctx, e, directoryReaders.DirectoryEntries, key.BasePackage)
	if err != nil {
		return PatchedPackagesAtAndBelowValue[TMetadata]{}, err
	}
	if !packageDirectory.IsSet() {
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.PackagesAtAndBelow_Value{}), nil
	}

	// Only return packages whose relative paths match the pattern.
	nfa, err := glob.NewNFAFromBytes(key.Pattern)
	if err != nil {
		return PatchedPackagesAtAndBelowValue[TMetadata]{}, fmt.Errorf("invalid pattern: %w", err)
	}
	var matcher glob.Matcher
	matcher.Initialize(nfa)

	// Find packages at and below the base package.
	checker := packageExistenceChecker[TReference]{
		context:          ctx,
		directoryReaders: directoryReaders,
	}
	packageAtBasePackage, err := directoryIsPackage(ctx, directoryReaders.DirectoryEntries, packageDirectory)
	if err != nil {
		return PatchedPackagesAtAndBelowValue[TMetadata]{}, err
	}
	if err := checker.findPackagesBelow(packageDirectory, nil, &matcher); err != nil {
		return PatchedPackagesAtAndBelowValue[TMetadata]{}, err
	}

	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.PackagesAtAndBelow_Value{
		PackageAtBasePackage:     packageAtBasePackage,
		PackagesBelowBasePackage: checker.packagesBelowBasePackage,
	}), nil
}

// PackagesAtAndBelowEnvironmentForTesting is used to generate mocks
// for testing ComputePackagesAtAndBelowValue().
type PackagesAtAndBelowEnvironmentForTesting PackagesAtAndBelowEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]

type packageExistenceChecker[TReference any] struct {
	context                  context.Context
	directoryReaders         *DirectoryReaders[TReference]
	packagesBelowBasePackage []string
}

// findPackagesBelow recursively traverses a directory hierarchy to find
// packages. Only packages whose relative path is matched are returned,
// and directories are only traversed if they may contain matching
// packages.
func (pec *packageExistenceChecker[TReference]) findPackagesBelow(d model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], dTrace *path.Trace, dMatcher *glob.Matcher) error {
	var childMatcher glob.Matcher

	var errIter error
MatchDirectories:
	for node, childDirectory := range model_filesystem.DirectoryAllChildren(pec.context, pec.directoryReaders.DirectoryEntries, d, &errIter) {
		name, ok := path.NewComponent(node.Message.Name)
		if !ok {
			return fmt.Errorf("invalid directory name %#v in directory %#v", node.Message.Name, dTrace.GetUNIXString())
		}
		childMatcher.CopyFrom(dMatcher)
		for _, r := range name.String() {
			if !childMatcher.WriteRune(r) {
				// Neither this directory nor any of
				// its children can match.
				continue MatchDirectories
			}
		}
		isMatch := childMatcher.IsMatch()
		mayContainMatches := childMatcher.WriteRune('/')
		if !isMatch && !mayContainMatches {
			continue MatchDirectories
		}
		childTrace := dTrace.Append(name)

		directoryIsPackage, err := directoryIsPackage(pec.context, pec.directoryReaders.DirectoryEntries, childDirectory)
		if err != nil {
			return fmt.Errorf("failed to determine whether directory %#v is a package: %w", dTrace.GetUNIXString(), err)
		}
		if directoryIsPackage {
			// Don't traverse into packages, as only
			// the nearest packages are returned.
			if isMatch {
				pec.packagesBelowBasePackage = append(pec.packagesBelowBasePackage, childTrace.GetUNIXString())
			}
		} else if mayContainMatches {
			// Not a package. Find packages below.
			if err := pec.findPackagesBelow(childDirectory, childTrace, &childMatcher); err != nil {
				return err
			}
		}
	}
	if errIter != nil {
		return fmt.Errorf("failed to iterate subdirectories of directory %#v: %w", dTrace.GetUNIXString(), errIter)
	}
	return nil
}

// directoryPackage returns true if the provided directory is the root
// directory of a package. A directory is a root directory of a package
// if it contains a BUILD.bazel or BUILD file.
func directoryIsPackage[TReference any](ctx context.Context, entriesReader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry], d model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]) (bool, error) {
	// TODO: Should we also consider symlinks having such names?
	for _, filename := range buildDotBazelTargetNames {
		node, _, err := model_filesystem.DirectoryLookupNode(ctx, entriesReader, d, path.MustNewComponent(filename.String()))
		if err != nil {
			return false, err
		}
		if node.IsSet() && node.Message.GetFile() != nil {
			// Current directory is a package.
			return true, nil
		}
	}
	return false, nil
}
