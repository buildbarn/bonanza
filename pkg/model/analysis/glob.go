package analysis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"bonanza.build/pkg/glob"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"
)

// globPathResolver is an implementation of path.ComponentWalker that
// resolves paths within a given package, for the purpose of expanding
// symbolic links encountered during glob() expansion.
type globPathResolver[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	walker  *globDirectoryWalker[TReference, TMetadata]
	stack   util.NonEmptyStack[model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]]
	gotFile bool
}

func (r *globPathResolver[TReference, TMetadata]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	w := r.walker
	d := r.stack.Peek()
	n := name.String()
	directories := d.Message.Directories
	if i, ok := sort.Find(
		len(directories),
		func(i int) int { return strings.Compare(n, directories[i].Name) },
	); ok {
		childDirectory, err := model_filesystem.DirectoryGetContents(
			w.context,
			w.directoryReaders.DirectoryContents,
			model_core.Nested(d, directories[i].Directory),
		)
		if err != nil {
			return nil, err
		}
		r.stack.Push(childDirectory)
		return path.GotDirectory{
			Child:        r,
			IsReversible: true,
		}, nil
	}

	leaves, err := model_filesystem.DirectoryGetLeaves(w.context, w.directoryReaders.Leaves, d)
	if err != nil {
		return nil, err
	}

	files := leaves.Message.Files
	if _, ok := sort.Find(
		len(files),
		func(i int) int { return strings.Compare(n, files[i].Name) },
	); ok {
		return nil, errors.New("path resolves to a regular file, while a directory was expected")
	}

	symlinks := leaves.Message.Symlinks
	if i, ok := sort.Find(
		len(symlinks),
		func(i int) int { return strings.Compare(n, symlinks[i].Name) },
	); ok {
		return path.GotSymlink{
			Parent: path.NewRelativeScopeWalker(r),
			Target: path.UNIXFormat.NewParser(symlinks[i].Target),
		}, nil
	}

	return nil, errors.New("path does not exist")
}

func (r *globPathResolver[TReference, TMetadata]) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	w := r.walker
	d := r.stack.Peek()
	n := name.String()
	directories := d.Message.Directories
	if i, ok := sort.Find(
		len(directories),
		func(i int) int { return strings.Compare(n, directories[i].Name) },
	); ok {
		childDirectory, err := model_filesystem.DirectoryGetContents(
			w.context,
			w.directoryReaders.DirectoryContents,
			model_core.Nested(d, directories[i].Directory),
		)
		if err != nil {
			return nil, err
		}
		r.stack.Push(childDirectory)
		return nil, nil
	}

	leaves, err := model_filesystem.DirectoryGetLeaves(w.context, w.directoryReaders.Leaves, d)
	if err != nil {
		return nil, err
	}

	files := leaves.Message.Files
	if _, ok := sort.Find(
		len(files),
		func(i int) int { return strings.Compare(n, files[i].Name) },
	); ok {
		r.gotFile = true
		return nil, nil
	}

	symlinks := leaves.Message.Symlinks
	if i, ok := sort.Find(
		len(symlinks),
		func(i int) int { return strings.Compare(n, symlinks[i].Name) },
	); ok {
		return &path.GotSymlink{
			Parent: path.NewRelativeScopeWalker(r),
			Target: path.UNIXFormat.NewParser(symlinks[i].Target),
		}, nil
	}

	return nil, errors.New("path does not exist")
}

func (r *globPathResolver[TReference, TMetadata]) OnUp() (path.ComponentWalker, error) {
	_, ok := r.stack.PopSingle()
	if !ok {
		return nil, errors.New("path escapes package root directory")
	}
	return r, nil
}

type globDirectoryWalker[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	context            context.Context
	computer           *baseComputer[TReference, TMetadata]
	directoryReaders   *DirectoryReaders[TReference]
	includeDirectories bool
	matchedPaths       []string
}

func (w *globDirectoryWalker[TReference, TMetadata]) walkDirectory(dStack util.NonEmptyStack[model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]], dPath *path.Trace, dMatcher *glob.Matcher) error {
	var childMatcher glob.Matcher
	d := dStack.Peek()

MatchDirectories:
	for _, entry := range d.Message.Directories {
		// Check whether the directory itself is a match.
		name, ok := path.NewComponent(entry.Name)
		if !ok {
			return fmt.Errorf("invalid name for directory %#v in directory %#v", entry.Name, dPath.GetUNIXString())
		}
		childMatcher.CopyFrom(dMatcher)
		for _, r := range name.String() {
			if !childMatcher.WriteRune(r) {
				continue MatchDirectories
			}
		}
		childPath := dPath.Append(name)
		if w.includeDirectories && childMatcher.IsMatch() {
			w.matchedPaths = append(w.matchedPaths, childPath.GetUNIXString())
		}

		// Traverse into the directory if the still reachable
		// part of the NFA contains positive end states.
		if childMatcher.WriteRune('/') {
			childDirectory, err := model_filesystem.DirectoryGetContents(
				w.context,
				w.directoryReaders.DirectoryContents,
				model_core.Nested(d, entry.Directory),
			)
			if err != nil {
				return fmt.Errorf("failed to get directory %#v", childPath.GetUNIXString())
			}

			dStack.Push(childDirectory)
			err = w.walkDirectory(dStack, childPath, &childMatcher)
			dStack.PopSingle()
			if err != nil {
				return err
			}
		}
	}

	leaves, err := model_filesystem.DirectoryGetLeaves(
		w.context,
		w.directoryReaders.Leaves,
		d,
	)
	if err != nil {
		return fmt.Errorf("failed to get leaves for directory %#v", dPath.GetUNIXString())
	}

MatchFiles:
	for _, entry := range leaves.Message.Files {
		name, ok := path.NewComponent(entry.Name)
		if !ok {
			return fmt.Errorf("invalid name for file %#v in directory %#v", entry.Name, dPath.GetUNIXString())
		}
		childMatcher.CopyFrom(dMatcher)
		for _, r := range name.String() {
			if !childMatcher.WriteRune(r) {
				continue MatchFiles
			}
		}
		if childMatcher.IsMatch() {
			w.matchedPaths = append(w.matchedPaths, dPath.Append(name).GetUNIXString())
		}
	}

MatchSymlinks:
	for _, entry := range leaves.Message.Symlinks {
		name, ok := path.NewComponent(entry.Name)
		if !ok {
			return fmt.Errorf("invalid name for symbolic link %#v in directory %#v", entry.Name, dPath.GetUNIXString())
		}
		childMatcher.CopyFrom(dMatcher)
		for _, r := range name.String() {
			if !childMatcher.WriteRune(r) {
				continue MatchSymlinks
			}
		}

		terminalMatch := childMatcher.IsMatch()
		directoryMatch := childMatcher.WriteRune('/')
		if terminalMatch || directoryMatch {
			// Symbolic link either matches the provided
			// pattern, or there may be paths below the
			// symbolic link that match. Resolve the target
			// of the symbolic link and continue traversal.
			r := globPathResolver[TReference, TMetadata]{
				walker: w,
				stack:  dStack.Copy(),
			}
			childPath := dPath.Append(name)
			if err := path.Resolve(
				path.UNIXFormat.NewParser(entry.Target),
				path.NewLoopDetectingScopeWalker(path.NewRelativeScopeWalker(&r)),
			); err != nil {
				return fmt.Errorf("failed to resolve target of symbolic link %#v: %w", childPath.GetUNIXString(), err)
			}

			if terminalMatch && (r.gotFile || w.includeDirectories) {
				// Symbolic link resolves to a regular
				// file, or we're instructed to match
				// directories as well.
				w.matchedPaths = append(w.matchedPaths, childPath.GetUNIXString())
			}
			if directoryMatch && !r.gotFile {
				// Symbolic link resolves to a
				// directory, and there may be paths
				// inside thatmatch the pattern.
				//
				// TODO: Add a recursion limit!
				if err := w.walkDirectory(r.stack, childPath, &childMatcher); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeGlobValue(ctx context.Context, key *model_analysis_pb.Glob_Key, e GlobEnvironment[TReference, TMetadata]) (PatchedGlobValue[TMetadata], error) {
	directoryReaders, gotDirectoryReaders := e.GetDirectoryReadersValue(&model_analysis_pb.DirectoryReaders_Key{})
	filesInPackageValue := e.GetFilesInPackageValue(&model_analysis_pb.FilesInPackage_Key{
		Package: key.Package,
	})
	if !gotDirectoryReaders || !filesInPackageValue.IsSet() {
		return PatchedGlobValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	nfa, err := glob.NewNFAFromBytes(key.Pattern)
	if err != nil {
		return PatchedGlobValue[TMetadata]{}, fmt.Errorf("invalid pattern: %w", err)
	}
	var matcher glob.Matcher
	matcher.Initialize(nfa)

	w := globDirectoryWalker[TReference, TMetadata]{
		context:            ctx,
		computer:           c,
		directoryReaders:   directoryReaders,
		includeDirectories: key.IncludeDirectories,
	}
	if err := w.walkDirectory(
		util.NewNonEmptyStack(
			model_core.Nested(filesInPackageValue, filesInPackageValue.Message.Directory),
		),
		nil,
		&matcher,
	); err != nil {
		return PatchedGlobValue[TMetadata]{}, err
	}

	// Bazel's implementation of glob() sorts results
	// alphabetically, with "/" being treated like an ordinary
	// character.
	sort.Strings(w.matchedPaths)

	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_analysis_pb.Glob_Value{
			MatchedPaths: w.matchedPaths,
		},
	), nil
}
