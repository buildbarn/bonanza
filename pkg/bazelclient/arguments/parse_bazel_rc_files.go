package arguments

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"math"
	"strings"
	"syscall"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type bazelRCPathResolver struct {
	openFile         bool
	ownedDirectories int
	stack            util.NonEmptyStack[filesystem.DirectoryCloser]
	openedFile       filesystem.FileReader
}

func (r *bazelRCPathResolver) closeOwned() {
	for r.ownedDirectories > 0 {
		d, ok := r.stack.PopSingle()
		if !ok {
			panic("incorrect owned directories count")
		}
		d.Close()
		r.ownedDirectories--
	}
}

func (r *bazelRCPathResolver) OnAbsolute() (path.ComponentWalker, error) {
	r.closeOwned()
	r.stack.PopAll()
	return r, nil
}

func (r *bazelRCPathResolver) OnRelative() (path.ComponentWalker, error) {
	return r, nil
}

func (bazelRCPathResolver) OnDriveLetter(drive rune) (path.ComponentWalker, error) {
	return nil, status.Error(codes.Unimplemented, "Drive letters not supported")
}

func (bazelRCPathResolver) OnShare(server, share string) (path.ComponentWalker, error) {
	return nil, status.Error(codes.Unimplemented, "Shares not supported")
}

func (r *bazelRCPathResolver) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	d := r.stack.Peek()
	child, err := d.EnterDirectory(name)
	if err != nil {
		if errors.Is(err, syscall.ENOTDIR) {
			if target, err := d.Readlink(name); err == nil {
				return path.GotSymlink{
					Parent: r,
					Target: target,
				}, nil
			}
		}
		return nil, err
	}

	r.stack.Push(child)
	r.ownedDirectories++
	return path.GotDirectory{
		Child:        r,
		IsReversible: true,
	}, nil
}

func (r *bazelRCPathResolver) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	if !r.openFile {
		return path.OnTerminalViaOnDirectory(r, name)
	}

	var err error
	d := r.stack.Peek()
	r.openedFile, err = d.OpenRead(name)
	if errors.Is(err, syscall.ELOOP) {
		if target, err := d.Readlink(name); err == nil {
			return &path.GotSymlink{
				Parent: r,
				Target: target,
			}, nil
		}
	}
	return nil, err
}

func (r *bazelRCPathResolver) OnUp() (path.ComponentWalker, error) {
	if d, ok := r.stack.PopSingle(); ok && r.ownedDirectories > 0 {
		if err := d.Close(); err != nil {
			r.stack.Push(d)
			return nil, err
		}
		r.ownedDirectories--
	}
	return r, nil
}

type bazelRCFileParser struct {
	pathFormat                  path.Format
	workspacePath               *string
	workingDirectoryDirectories util.NonEmptyStack[filesystem.DirectoryCloser]
	workingDirectoryPath        *path.Builder
	directives                  ConfigurationDirectives
	importedPaths               map[string]struct{}
}

func (p *bazelRCFileParser) parseRecursively(bazelRCPath path.Parser, required bool) error {
	// Resolve and open the bazelrc file.
	opener := bazelRCPathResolver{
		openFile: true,
		stack:    p.workingDirectoryDirectories.Copy(),
	}
	defer opener.closeOwned()

	resolvedPath, scopeWalker := p.workingDirectoryPath.Join(&opener)
	errResolve := path.Resolve(bazelRCPath, path.NewLoopDetectingScopeWalker(scopeWalker))

	resolvedPathStr, err := p.pathFormat.GetString(resolvedPath)
	if err != nil {
		return util.StatusWrap(err, "No valid string representation for import path")
	}

	if errResolve != nil {
		if errors.Is(errResolve, fs.ErrNotExist) {
			if required {
				return util.StatusWrapfWithCode(errResolve, codes.NotFound, "Directory %#v", resolvedPathStr)
			}
			return nil
		}
		return util.StatusWrapf(errResolve, "Directory %#v", resolvedPathStr)
	}

	f := opener.openedFile
	if f == nil {
		return status.Errorf(codes.InvalidArgument, "Path %#v resolves to a directory", resolvedPathStr)
	}
	defer f.Close()

	// Disallow cyclic imports.
	if _, ok := p.importedPaths[resolvedPathStr]; ok {
		return status.Errorf(codes.InvalidArgument, "Cyclic import of %#v", resolvedPathStr)
	}
	p.importedPaths[resolvedPathStr] = struct{}{}
	defer func() {
		delete(p.importedPaths, resolvedPathStr)
	}()

	// Process all directives in this file.
	r := NewBazelRCReader(bufio.NewReader(io.NewSectionReader(f, 0, math.MaxInt64)))
	for {
		fields, err := r.Read()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		if fields[0] == "import" || fields[0] == "try-import" {
			if len(fields) != 2 {
				return status.Errorf(codes.InvalidArgument, "File %#v contains an invalid import statement", resolvedPathStr)
			}

			importPath := fields[1]
			if p.workspacePath != nil {
				importPath = strings.ReplaceAll(importPath, "%workspace%", *p.workspacePath)
			}

			if err := p.parseRecursively(
				p.pathFormat.NewParser(importPath),
				fields[0] == "import",
			); err != nil {
				return util.StatusWrapf(err, "In file %#v", resolvedPathStr)
			}
		} else {
			p.directives[fields[0]] = append(p.directives[fields[0]], fields[1:])
		}
	}
}

// ConfigurationDirectives contains the set of directives that were
// extracted from bazelrc files by ParseBazelRCFiles().
type ConfigurationDirectives map[string][][]string

// ParseBazelRCFiles parses a list of bazelrc files, and returns the set
// of directives contained within.
func ParseBazelRCFiles(bazelRCPaths []BazelRCPath, rootDirectory filesystem.Directory, pathFormat path.Format, workspacePath, workingDirectoryPath path.Parser) (ConfigurationDirectives, error) {
	if len(bazelRCPaths) == 0 {
		return ConfigurationDirectives{}, nil
	}

	// Resolve the workspace and working directory paths.
	var workspacePathValue *string
	if workspacePath != nil {
		workspacePathBuilder, scopeWalker := path.RootBuilder.Join(path.NewAbsoluteScopeWalker(path.VoidComponentWalker))
		if err := path.Resolve(workspacePath, scopeWalker); err != nil {
			return nil, util.StatusWrap(err, "Failed to resolve workspace path")
		}
		workspacePathStr, err := pathFormat.GetString(workspacePathBuilder)
		if err != nil {
			return nil, util.StatusWrap(err, "Failed to resolve workspace path")
		}
		workspacePathValue = &workspacePathStr
	}

	workingDirectoryResolver := bazelRCPathResolver{
		stack: util.NewNonEmptyStack(filesystem.NopDirectoryCloser(rootDirectory)),
	}
	defer workingDirectoryResolver.closeOwned()

	workingDirectoryPathBuilder, scopeWalker := path.RootBuilder.Join(path.NewAbsoluteScopeWalker(&workingDirectoryResolver))
	if err := path.Resolve(workingDirectoryPath, path.NewLoopDetectingScopeWalker(scopeWalker)); err != nil {
		return nil, util.StatusWrap(err, "Failed to resolve working directory path")
	}

	// Parse each of the provided files, while recursively loading
	// any files they import.
	p := bazelRCFileParser{
		pathFormat:                  pathFormat,
		workspacePath:               workspacePathValue,
		workingDirectoryDirectories: workingDirectoryResolver.stack,
		workingDirectoryPath:        workingDirectoryPathBuilder,
		directives:                  ConfigurationDirectives{},
		importedPaths:               map[string]struct{}{},
	}
	for _, bazelRCPath := range bazelRCPaths {
		if err := p.parseRecursively(
			bazelRCPath.Path,
			bazelRCPath.Required,
		); err != nil {
			return nil, err
		}
	}
	return p.directives, nil
}
