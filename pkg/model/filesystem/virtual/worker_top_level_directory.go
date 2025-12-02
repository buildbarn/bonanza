package virtual

import (
	"context"
	"sync"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
)

// WorkerTopLevelDirectory is the top-level directory that is exposed
// through bonanza_worker's virtual file system. When accessed through
// the virtual file system it behaves like a simple read-only directory.
// However, worker threads are capable of temporarily adding child
// directories to it, in which execution of build actions takes place.
type WorkerTopLevelDirectory struct {
	virtual.ReadOnlyDirectory
	handle virtual.StatefulDirectoryHandle

	lock        sync.RWMutex
	children    map[path.Component]virtual.DirectoryChild
	changeID    uint64
	removalWait chan struct{}
}

var _ virtual.Directory = &WorkerTopLevelDirectory{}

// NewWorkerTopLevelDirectory creates a new bonanza_worker top-level
// build directory that is empty.
func NewWorkerTopLevelDirectory(handleAllocation virtual.StatefulHandleAllocation) *WorkerTopLevelDirectory {
	d := &WorkerTopLevelDirectory{
		children: map[path.Component]virtual.DirectoryChild{},
	}
	d.handle = handleAllocation.AsStatefulDirectory(d)
	return d
}

// AddChild adds a child directory to the worker top-level directory.
//
// If the directory already exists, this function blocks until that
// directory is removed. This is done so that multiple worker threads
// can safely run actions that require the same stable input root path,
// at the cost of those actions running sequentially. To prevent actions
// from running sequentially, workers should use file system namespace
// virtualization (i.e., containers, jails) where possible.
func (d *WorkerTopLevelDirectory) AddChild(ctx context.Context, name path.Component, child virtual.DirectoryChild) error {
	for {
		d.lock.Lock()
		if _, ok := d.children[name]; !ok {
			d.children[name] = child
			d.changeID++
			d.lock.Unlock()
			return nil
		}
		if d.removalWait == nil {
			d.removalWait = make(chan struct{})
		}
		w := d.removalWait
		d.lock.Unlock()
		<-w
	}
}

// RemoveChild removes a child directory from a top-level directory.
func (d *WorkerTopLevelDirectory) RemoveChild(name path.Component) {
	d.lock.Lock()
	delete(d.children, name)
	d.changeID++
	if d.removalWait != nil {
		close(d.removalWait)
		d.removalWait = nil
	}
	d.lock.Unlock()

	d.handle.NotifyRemoval(name)
}

// VirtualGetAttributes returns file system attributes of the worker
// top-level directory, such as permissions, size, and link count.
func (d *WorkerTopLevelDirectory) VirtualGetAttributes(ctx context.Context, requested virtual.AttributesMask, attributes *virtual.Attributes) {
	attributes.SetFileType(filesystem.FileTypeDirectory)
	attributes.SetHasNamedAttributes(false)
	attributes.SetIsInNamedAttributeDirectory(false)
	attributes.SetPermissions(virtual.PermissionsExecute)
	attributes.SetSizeBytes(0)
	if requested&(virtual.AttributesMaskChangeID|virtual.AttributesMaskLinkCount) != 0 {
		d.lock.RLock()
		attributes.SetChangeID(d.changeID)
		attributes.SetLinkCount(virtual.EmptyDirectoryLinkCount + uint32(len(d.children)))
		d.lock.RUnlock()
	}
	d.handle.GetAttributes(requested, attributes)
}

// VirtualLookup looks up a build directory that was added to the worker
// top-level directory through a call to AddChild().
func (d *WorkerTopLevelDirectory) VirtualLookup(ctx context.Context, name path.Component, requested virtual.AttributesMask, out *virtual.Attributes) (virtual.DirectoryChild, virtual.Status) {
	d.lock.RLock()
	child, ok := d.children[name]
	d.lock.RUnlock()
	if !ok {
		return virtual.DirectoryChild{}, virtual.StatusErrNoEnt
	}
	child.GetNode().VirtualGetAttributes(ctx, requested, out)
	return child, virtual.StatusOK
}

// VirtualOpenChild creates or opens a regular file contained in a
// worker top-level directory. As this directory can only contain
// subdirectories and not files, this always fails.
func (WorkerTopLevelDirectory) VirtualOpenChild(ctx context.Context, name path.Component, shareAccess virtual.ShareMask, createAttributes *virtual.Attributes, existingOptions *virtual.OpenExistingOptions, requested virtual.AttributesMask, openedFileAttributes *virtual.Attributes) (virtual.Leaf, virtual.AttributesMask, virtual.ChangeInfo, virtual.Status) {
	return virtual.ReadOnlyDirectoryOpenChildDoesntExist(createAttributes)
}

// VirtualReadDir returns a list of directories contained in the worker
// top-level directory. These intentionally fail with EACCES, so that
// actions can't see build directories belonging to other actions.
func (WorkerTopLevelDirectory) VirtualReadDir(ctx context.Context, firstCookie uint64, requested virtual.AttributesMask, reporter virtual.DirectoryEntryReporter) virtual.Status {
	return virtual.StatusErrAccess
}

// VirtualApply can be used to perform custom operations against the
// worker top-level directory. This directory type does not provide any
// custom operations.
func (WorkerTopLevelDirectory) VirtualApply(data any) bool {
	return false
}

// VirtualOpenNamedAttributes can be used to access named attributes of
// the worker top-level directory. This directory type is immutable and
// does not provide any named attributes of its own, meaning this method
// always fails.
func (WorkerTopLevelDirectory) VirtualOpenNamedAttributes(ctx context.Context, createDirectory bool, requested virtual.AttributesMask, attributes *virtual.Attributes) (virtual.Directory, virtual.Status) {
	if createDirectory {
		return nil, virtual.StatusErrAccess
	}
	return nil, virtual.StatusErrNoEnt
}
