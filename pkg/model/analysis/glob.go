package analysis

import (
	"context"
	"errors"
	"fmt"
	"sort"

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
	stack   util.NonEmptyStack[model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]]
	gotFile bool
}

// lookupNode looks up a node in the current directory. If the node is
// a directory, its contents are pushed onto the stack.
func (r *globPathResolver[TReference, TMetadata]) lookupNode(name path.Component) (*model_filesystem_pb.DirectoryEntry_Node, error) {
	w := r.walker
	d := r.stack.Peek()
	node, directoryIndex, err := model_filesystem.DirectoryLookupNode(w.context, w.directoryReaders.DirectoryEntries, d, name)
	if err != nil {
		return nil, err
	}
	if !node.IsSet() {
		return nil, errors.New("path does not exist")
	}
	if node.Message.GetDirectory() != nil {
		childDirectory, err := model_filesystem.DirectoryGetChild(w.context, w.directoryReaders.DirectoryEntries, d, directoryIndex)
		if err != nil {
			return nil, err
		}
		r.stack.Push(childDirectory)
	}
	return node.Message, nil
}

func (r *globPathResolver[TReference, TMetadata]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	node, err := r.lookupNode(name)
	if err != nil {
		return nil, err
	}
	switch node.WhichKind() {
	case model_filesystem_pb.DirectoryEntry_Node_Directory_case:
		return path.GotDirectory{
			Child:        r,
			IsReversible: true,
		}, nil
	case model_filesystem_pb.DirectoryEntry_Node_Symlink_case:
		return path.GotSymlink{
			Parent: path.NewRelativeScopeWalker(r),
			Target: path.UNIXFormat.NewParser(node.GetSymlink().GetTarget()),
		}, nil
	default:
		return nil, errors.New("path resolves to a regular file, while a directory was expected")
	}
}

func (r *globPathResolver[TReference, TMetadata]) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	node, err := r.lookupNode(name)
	if err != nil {
		return nil, err
	}
	switch node.WhichKind() {
	case model_filesystem_pb.DirectoryEntry_Node_Directory_case:
		return nil, nil
	case model_filesystem_pb.DirectoryEntry_Node_Symlink_case:
		return &path.GotSymlink{
			Parent: path.NewRelativeScopeWalker(r),
			Target: path.UNIXFormat.NewParser(node.GetSymlink().GetTarget()),
		}, nil
	default:
		r.gotFile = true
		return nil, nil
	}
}

func (r *globPathResolver[TReference, TMetadata]) OnUp() (path.ComponentWalker, error) {
	_, ok := r.stack.PopSingle()
	if !ok {
		return nil, errors.New("path escapes package root directory")
	}
	return r, nil
}

// globMatcherWriteString writes all characters of a string into a
// glob matcher. It returns false if the matcher can no longer match.
func globMatcherWriteString(m *glob.Matcher, s string) bool {
	for _, r := range s {
		if !m.WriteRune(r) {
			return false
		}
	}
	return true
}

type globDirectoryWalker[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	context            context.Context
	computer           *baseComputer[TReference, TMetadata]
	directoryReaders   *DirectoryReaders[TReference]
	includeDirectories bool
	matchedPaths       []string
}

func (w *globDirectoryWalker[TReference, TMetadata]) walkDirectory(dStack util.NonEmptyStack[model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]], dPath *path.Trace, dMatcher *glob.Matcher) error {
	var childMatcher glob.Matcher
	d := dStack.Peek()

	var errIter error
	directoryIndex := uint64(0)
MatchNodes:
	for node := range model_filesystem.DirectoryAllNodes(w.context, w.directoryReaders.DirectoryEntries, d, &errIter) {
		name, ok := path.NewComponent(node.Message.GetName())
		if !ok {
			return fmt.Errorf("invalid name for node %#v in directory %#v", node.Message.GetName(), dPath.GetUNIXString())
		}
		childMatcher.CopyFrom(dMatcher)
		childMatches := globMatcherWriteString(&childMatcher, name.String())
		switch node.Message.WhichKind() {
		case model_filesystem_pb.DirectoryEntry_Node_Directory_case:
			childDirectoryIndex := directoryIndex
			directoryIndex++
			if !childMatches {
				continue MatchNodes
			}

			// Check whether the directory itself is a match.
			childPath := dPath.Append(name)
			if w.includeDirectories && childMatcher.IsMatch() {
				w.matchedPaths = append(w.matchedPaths, childPath.GetUNIXString())
			}

			// Traverse into the directory if the still
			// reachable part of the NFA contains positive
			// end states.
			if childMatcher.WriteRune('/') {
				childDirectory, err := model_filesystem.DirectoryGetChild(
					w.context,
					w.directoryReaders.DirectoryEntries,
					d,
					childDirectoryIndex,
				)
				if err != nil {
					return fmt.Errorf("failed to get directory %#v: %w", childPath.GetUNIXString(), err)
				}

				dStack.Push(childDirectory)
				err = w.walkDirectory(dStack, childPath, &childMatcher)
				dStack.PopSingle()
				if err != nil {
					return err
				}
			}

		case model_filesystem_pb.DirectoryEntry_Node_File_case:
			if childMatches && childMatcher.IsMatch() {
				w.matchedPaths = append(w.matchedPaths, dPath.Append(name).GetUNIXString())
			}

		case model_filesystem_pb.DirectoryEntry_Node_Symlink_case:
			if !childMatches {
				continue MatchNodes
			}
			terminalMatch := childMatcher.IsMatch()
			directoryMatch := childMatcher.WriteRune('/')
			if terminalMatch || directoryMatch {
				// Symbolic link either matches the
				// provided pattern, or there may be
				// paths below the symbolic link that
				// match. Resolve the target of the
				// symbolic link and continue traversal.
				r := globPathResolver[TReference, TMetadata]{
					walker: w,
					stack:  dStack.Copy(),
				}
				childPath := dPath.Append(name)
				if err := path.Resolve(
					path.UNIXFormat.NewParser(node.Message.GetSymlink().GetTarget()),
					path.NewLoopDetectingScopeWalker(path.NewRelativeScopeWalker(&r)),
				); err != nil {
					return fmt.Errorf("failed to resolve target of symbolic link %#v: %w", childPath.GetUNIXString(), err)
				}

				if terminalMatch && (r.gotFile || w.includeDirectories) {
					// Symbolic link resolves to a
					// regular file, or we're
					// instructed to match
					// directories as well.
					w.matchedPaths = append(w.matchedPaths, childPath.GetUNIXString())
				}
				if directoryMatch && !r.gotFile {
					// Symbolic link resolves to a
					// directory, and there may be
					// paths inside that match the
					// pattern.
					//
					// TODO: Add a recursion limit!
					if err := w.walkDirectory(r.stack, childPath, &childMatcher); err != nil {
						return err
					}
				}
			}
		}
	}
	if errIter != nil {
		return fmt.Errorf("failed to iterate directory %#v: %w", dPath.GetUNIXString(), errIter)
	}
	return nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeGlobValue(ctx context.Context, key *model_analysis_pb.Glob_Key, e GlobEnvironment[TReference, TMetadata]) (PatchedGlobValue[TMetadata], error) {
	directoryReaders, gotDirectoryReaders := e.GetDirectoryAccessParametersObject(&model_analysis_pb.DirectoryAccessParameters_Key{})
	filesInPackageValue := e.GetFilesInPackageValue(model_analysis_pb.FilesInPackage_Key_builder{
		Package: key.GetPackage(),
	}.Build())
	if !gotDirectoryReaders || !filesInPackageValue.IsSet() {
		return PatchedGlobValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	nfa, err := glob.NewNFAFromBytes(key.GetPattern())
	if err != nil {
		return PatchedGlobValue[TMetadata]{}, fmt.Errorf("invalid pattern: %w", err)
	}
	var matcher glob.Matcher
	matcher.Initialize(nfa)

	w := globDirectoryWalker[TReference, TMetadata]{
		context:            ctx,
		computer:           c,
		directoryReaders:   directoryReaders,
		includeDirectories: key.GetIncludeDirectories(),
	}
	if err := w.walkDirectory(
		util.NewNonEmptyStack(
			model_core.Nested(filesInPackageValue, filesInPackageValue.Message.GetDirectory()),
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

	return model_core.NewSimplePatchedMessage[TMetadata](model_analysis_pb.Glob_Value_builder{
		MatchedPaths: w.matchedPaths,
	}.Build()), nil
}
