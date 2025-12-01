package starlark

import (
	"errors"
	"fmt"
	"strings"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/starlark/unpack"

	bb_path "github.com/buildbarn/bb-storage/pkg/filesystem/path"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// BarePath stores an absolute pathname contained in a Starlark path
// object.
type BarePath struct {
	parent    *BarePath
	component bb_path.Component
}

var _ bb_path.Parser = (*BarePath)(nil)

// Append a pathname component to a BarePath. The existing path remains
// unmodified, and a new BarePath is returned.
func (bp *BarePath) Append(component bb_path.Component) *BarePath {
	return &BarePath{
		parent:    bp,
		component: component,
	}
}

// ParseScope starts a forward traversal of the components of a
// BarePath. This method is provided to allow passing instances of
// BarePath to functions like path.Resolve(), so that path traversal can
// take place.
func (bp *BarePath) ParseScope(scopeWalker bb_path.ScopeWalker) (bb_path.ComponentWalker, bb_path.RelativeParser, error) {
	next, err := scopeWalker.OnAbsolute()
	if err != nil {
		return nil, nil, err
	}

	// Obtain all components of the path.
	count := 0
	for bpCount := bp; bpCount != nil; bpCount = bpCount.parent {
		count++
	}
	components := make([]bb_path.Component, count)
	for count > 0 {
		count--
		components[count] = bp.component
		bp = bp.parent
	}

	return next, pathComponentParser{
		components: components,
	}, nil
}

func (bp *BarePath) writeToStringBuilder(sb *strings.Builder) {
	if bp.parent != nil {
		bp.parent.writeToStringBuilder(sb)
	}
	sb.WriteByte('/')
	sb.WriteString(bp.component.String())
}

func (bp *BarePath) getLength() int {
	length := 0
	for bp != nil {
		length++
		bp = bp.parent
	}
	return length
}

// GetRelativeTo returns whether the receiving path is below another
// path. If so, it returns a non-nil slice of the components of the
// trailing part of the path that is below the other. If not, it returns
// nil.
func (bp *BarePath) GetRelativeTo(other *BarePath) []bb_path.Component {
	// 'bp' can only be below 'other' if it has at least as many
	// components.
	bpLength, otherLength := bp.getLength(), other.getLength()
	if bpLength < otherLength {
		return nil
	}

	// Extract trailing components of 'bp', so that both 'bp' and
	// 'other' become the same length.
	delta := bpLength - otherLength
	trailingComponents := make([]bb_path.Component, delta)
	for delta > 0 {
		trailingComponents[delta-1] = bp.component
		bp = bp.parent
		delta--
	}

	// All remaining components must be equal to each other.
	for bp != nil {
		if bp.component != other.component {
			return nil
		}
		bp, other = bp.parent, other.parent
	}
	return trailingComponents
}

// GetUNIXString returns a BarePath in the form of a UNIX-style pathname
// string.
func (bp *BarePath) GetUNIXString() string {
	if bp == nil {
		return "/"
	}
	var sb strings.Builder
	bp.writeToStringBuilder(&sb)
	return sb.String()
}

// Filesystem is called into by path objects when properties are
// accessed that depend on the current state of the file system.
type Filesystem interface {
	Exists(*BarePath) (bool, error)
	IsDir(*BarePath) (bool, error)
	Readdir(*BarePath) ([]bb_path.Component, error)
	Realpath(*BarePath) (*BarePath, error)
}

type path struct {
	bare       *BarePath
	filesystem Filesystem
}

var (
	_ starlark.Value    = (*path)(nil)
	_ starlark.HasAttrs = (*path)(nil)
)

// NewPath creates a Starlark path object. These objects consist of a
// bare path that represents an absolute path, and a filesystem in which
// operations performed against these paths should take place.
func NewPath(bp *BarePath, filesystem Filesystem) starlark.Value {
	return &path{
		bare:       bp,
		filesystem: filesystem,
	}
}

func (p *path) String() string {
	return p.bare.GetUNIXString()
}

func (path) Type() string {
	return "path"
}

func (path) Freeze() {}

func (path) Truth() starlark.Bool {
	return starlark.True
}

func (path) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("path cannot be hashed")
}

func (p *path) Attr(thread *starlark.Thread, name string) (starlark.Value, error) {
	bp := p.bare
	switch name {
	case "basename":
		if bp == nil {
			return starlark.None, nil
		}
		return starlark.String(bp.component.String()), nil
	case "dirname":
		if bp == nil {
			return starlark.None, nil
		}
		return NewPath(bp.parent, p.filesystem), nil
	case "exists":
		exists, err := p.filesystem.Exists(p.bare)
		if err != nil {
			return nil, err
		}
		return starlark.Bool(exists), nil
	case "get_child":
		return starlark.NewBuiltin("path.get_child", p.doGetChild), nil
	case "is_dir":
		isDir, err := p.filesystem.IsDir(p.bare)
		if err != nil {
			return nil, err
		}
		return starlark.Bool(isDir), nil
	case "readdir":
		return starlark.NewBuiltin("path.readdir", p.doReaddir), nil
	case "realpath":
		realpath, err := p.filesystem.Realpath(p.bare)
		if err != nil {
			return nil, err
		}
		return NewPath(realpath, p.filesystem), nil
	default:
		return nil, nil
	}
}

var pathAttrNames = []string{
	"basename",
	"dirname",
	"exists",
	"get_child",
	"is_dir",
	"readdir",
	"realpath",
}

func (path) AttrNames() []string {
	return pathAttrNames
}

func (p *path) doGetChild(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(kwargs) != 0 {
		return nil, fmt.Errorf("%s: got %d keyword arguments, want 0", b.Name(), len(kwargs))
	}

	resolver := PathResolver{
		CurrentPath: p.bare,
	}
	for i, relativePath := range args {
		relativePathStr, ok := starlark.AsString(relativePath)
		if !ok {
			return nil, fmt.Errorf("at index %d: got %d, want string", i, relativePath.Type())
		}
		if err := bb_path.Resolve(
			bb_path.UNIXFormat.NewParser(relativePathStr),
			bb_path.NewRelativeScopeWalker(&resolver),
		); err != nil {
			return nil, fmt.Errorf("failed to resolve path %#v: %w", relativePathStr)
		}
	}

	return NewPath(resolver.CurrentPath, p.filesystem), nil
}

func (p *path) doReaddir(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var watch string
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"watch?", unpack.Bind(thread, &watch, unpack.String),
	); err != nil {
		return nil, err
	}

	names, err := p.filesystem.Readdir(p.bare)
	if err != nil {
		return nil, err
	}
	paths := make([]starlark.Value, 0, len(names))
	for _, name := range names {
		paths = append(paths, NewPath(p.bare.Append(name), p.filesystem))
	}
	return starlark.NewList(paths), nil
}

type pathComponentParser struct {
	components []bb_path.Component
}

func (pcp pathComponentParser) ParseFirstComponent(componentWalker bb_path.ComponentWalker, mustBeDirectory bool) (next bb_path.GotDirectoryOrSymlink, remainder bb_path.RelativeParser, err error) {
	// Stop parsing if there are no components left.
	if len(pcp.components) == 0 {
		return bb_path.GotDirectory{Child: componentWalker}, nil, nil
	}
	name := pcp.components[0]
	remainder = pathComponentParser{
		components: pcp.components[1:],
	}

	// Call one of OnDirectory() or OnTerminal(), depending on the
	// component's location in the path.
	if len(pcp.components) > 1 || mustBeDirectory {
		r, err := componentWalker.OnDirectory(name)
		return r, remainder, err
	}
	r, err := componentWalker.OnTerminal(name)
	if err != nil || r == nil {
		return nil, nil, err
	}
	return r, nil, nil
}

// PathResolver can be used in conjunction with path.Resolve() to
// convert pathname strings to BarePath objects, or to resolve a path
// relative to an existing BarePath.
type PathResolver struct {
	CurrentPath *BarePath
}

var (
	_ bb_path.ScopeWalker     = (*PathResolver)(nil)
	_ bb_path.ComponentWalker = (*PathResolver)(nil)
)

// OnAbsolute is invoked when a leading slash is encountered during
// pathname resolution.
func (r *PathResolver) OnAbsolute() (bb_path.ComponentWalker, error) {
	r.CurrentPath = nil
	return r, nil
}

// OnRelative is invoked when pathname resolution is performed against a
// relative path.
func (r *PathResolver) OnRelative() (bb_path.ComponentWalker, error) {
	return r, nil
}

// OnDriveLetter is invoked when a Windows drive letter is encountered
// during pathname resolution.
func (r *PathResolver) OnDriveLetter(drive rune) (bb_path.ComponentWalker, error) {
	r.CurrentPath = nil
	return r, nil
}

// OnShare is invoked when a Windows network share prefix is encountered
// during pathname resolution.
func (r *PathResolver) OnShare(server, share string) (bb_path.ComponentWalker, error) {
	r.CurrentPath = nil
	return r, nil
}

// OnDirectory is invoked when leading pathname components are
// encountered during path resolution.
func (r *PathResolver) OnDirectory(name bb_path.Component) (bb_path.GotDirectoryOrSymlink, error) {
	r.CurrentPath = r.CurrentPath.Append(name)
	return bb_path.GotDirectory{
		Child:        r,
		IsReversible: true,
	}, nil
}

// OnTerminal is invoked when the final pathname component is
// encountered during path resolution.
func (r *PathResolver) OnTerminal(name bb_path.Component) (*bb_path.GotSymlink, error) {
	r.CurrentPath = r.CurrentPath.Append(name)
	return nil, nil
}

// OnUp is invoked when a ".." pathname component is encountered during
// path resolution. Bazel aggressively prunes ".." components from
// paths, even if it leads to incorrect results when symlinks are used.
func (r *PathResolver) OnUp() (bb_path.ComponentWalker, error) {
	if r.CurrentPath != nil {
		r.CurrentPath = r.CurrentPath.parent
	}
	return r, nil
}

// RepoPathResolver is called into by unpackers created using
// NewPathOrLabelOrStringUnpackerInto to obtain the absolute path at
// which a repo is stored on the file system. This is needed to obtain
// the absolute path of a file when a Label is provided.
type RepoPathResolver func(canonicalRepo pg_label.CanonicalRepo) (*BarePath, error)

type pathOrLabelOrStringUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	repoPathResolver RepoPathResolver
	workingDirectory *BarePath
}

// NewPathOrLabelOrStringUnpackerInto creates an unpacker for Starlark
// function arguments that either accepts a path object, a Label, or a
// pathname string. This is typically used by the methods of module_ctx
// and repository_ctx, which accepts pathname strings in various
// formats. All values are converted to absolute paths.
func NewPathOrLabelOrStringUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata](repoPathResolver RepoPathResolver, workingDirectory *BarePath) unpack.UnpackerInto[*BarePath] {
	return &pathOrLabelOrStringUnpackerInto[TReference, TMetadata]{
		repoPathResolver: repoPathResolver,
		workingDirectory: workingDirectory,
	}
}

func (ui *pathOrLabelOrStringUnpackerInto[TReference, TMetadata]) UnpackInto(thread *starlark.Thread, v starlark.Value, dst **BarePath) error {
	switch typedV := v.(type) {
	case starlark.String:
		r := PathResolver{
			CurrentPath: ui.workingDirectory,
		}
		if err := bb_path.Resolve(bb_path.UNIXFormat.NewParser(string(typedV)), &r); err != nil {
			return err
		}
		*dst = r.CurrentPath
		return nil
	case Label[TReference, TMetadata]:
		canonicalLabel, err := typedV.value.AsCanonical()
		if err != nil {
			return err
		}
		bp, err := ui.repoPathResolver(canonicalLabel.GetCanonicalRepo())
		if err != nil {
			return err
		}
		for _, component := range strings.Split(canonicalLabel.GetRepoRelativePath(), "/") {
			bp = bp.Append(bb_path.MustNewComponent(component))
		}
		*dst = bp
		return nil
	case *path:
		*dst = typedV.bare
		return nil
	default:
		return fmt.Errorf("got %s, want path, Label or str", v.Type())
	}
}

func (ui *pathOrLabelOrStringUnpackerInto[TReference, TMetadata]) Canonicalize(thread *starlark.Thread, v starlark.Value) (starlark.Value, error) {
	var bp *BarePath
	if err := ui.UnpackInto(thread, v, &bp); err != nil {
		return nil, err
	}
	return starlark.String(bp.GetUNIXString()), nil
}

func (pathOrLabelOrStringUnpackerInto[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return 0
}
