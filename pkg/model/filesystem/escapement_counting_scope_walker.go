package filesystem

import (
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

// EscapementCountingScopeWalker determines the number of levels a given
// path escapes the current directory. This is achieved by counting the
// number of leading ".." components.
//
// If the path is absolute or if ".." components are placed after named
// components (e.g., "a/../b"), it determines the number of levels is
// not bounded.
type EscapementCountingScopeWalker struct {
	levels *wrapperspb.UInt32Value
}

var _ path.ScopeWalker = (*EscapementCountingScopeWalker)(nil)

// NewEscapementCountingScopeWalker creates an
// EscapementCountingScopeWalker that is in the initial path parsing
// state (i.e., corresponding with path ".").
func NewEscapementCountingScopeWalker() *EscapementCountingScopeWalker {
	return &EscapementCountingScopeWalker{
		levels: &wrapperspb.UInt32Value{Value: 0},
	}
}

// GetLevels returns the number of levels the parsed path escapes the
// current directory. If nil is returned, the escapement is not bounded.
func (sw *EscapementCountingScopeWalker) GetLevels() *wrapperspb.UInt32Value {
	return sw.levels
}

// OnAbsolute is called when the path for which the number of escapement
// levels is computed starts with a slash.
func (sw *EscapementCountingScopeWalker) OnAbsolute() (path.ComponentWalker, error) {
	sw.levels = nil
	return path.VoidComponentWalker, nil
}

// OnRelative is called when the path for which the number of escapement
// levels is computed is relative.
func (sw *EscapementCountingScopeWalker) OnRelative() (path.ComponentWalker, error) {
	return leadingEscapementCountingComponentWalker{
		scopeWalker: sw,
	}, nil
}

// OnDriveLetter is called when the path for which the number of
// escapement levels is computed starts with a Windows drive letter.
func (sw *EscapementCountingScopeWalker) OnDriveLetter(drive rune) (path.ComponentWalker, error) {
	sw.levels = nil
	return path.VoidComponentWalker, nil
}

// OnShare is called when the path for which the number of escapement
// levels is computed starts with a Windows network share.
func (sw *EscapementCountingScopeWalker) OnShare(server, share string) (path.ComponentWalker, error) {
	sw.levels = nil
	return path.VoidComponentWalker, nil
}

type leadingEscapementCountingComponentWalker struct {
	scopeWalker *EscapementCountingScopeWalker
}

func (cw leadingEscapementCountingComponentWalker) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	return path.GotDirectory{
		Child: subsequentEscapementCountingComponentWalker{
			scopeWalker: cw.scopeWalker,
		},
		IsReversible: false,
	}, nil
}

func (leadingEscapementCountingComponentWalker) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	return nil, nil
}

func (cw leadingEscapementCountingComponentWalker) OnUp() (path.ComponentWalker, error) {
	cw.scopeWalker.levels.Value++
	return cw, nil
}

type subsequentEscapementCountingComponentWalker struct {
	scopeWalker *EscapementCountingScopeWalker
}

func (cw subsequentEscapementCountingComponentWalker) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	return path.GotDirectory{
		Child:        cw,
		IsReversible: false,
	}, nil
}

func (subsequentEscapementCountingComponentWalker) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	return nil, nil
}

func (cw subsequentEscapementCountingComponentWalker) OnUp() (path.ComponentWalker, error) {
	cw.scopeWalker.levels = nil
	return path.VoidComponentWalker, nil
}
