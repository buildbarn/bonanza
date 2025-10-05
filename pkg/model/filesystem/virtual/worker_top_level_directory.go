package virtual

import (
	"context"
	"sync"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
)

type WorkerTopLevelDirectory struct {
	virtual.ReadOnlyDirectory
	handle virtual.StatefulDirectoryHandle

	lock        sync.RWMutex
	children    map[path.Component]virtual.DirectoryChild
	changeID    uint64
	removalWait chan struct{}
}

var _ virtual.Directory = &WorkerTopLevelDirectory{}

func NewWorkerTopLevelDirectory(handleAllocation virtual.StatefulHandleAllocation) *WorkerTopLevelDirectory {
	d := &WorkerTopLevelDirectory{
		children: map[path.Component]virtual.DirectoryChild{},
	}
	d.handle = handleAllocation.AsStatefulDirectory(d)
	return d
}

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

func (WorkerTopLevelDirectory) VirtualOpenChild(ctx context.Context, name path.Component, shareAccess virtual.ShareMask, createAttributes *virtual.Attributes, existingOptions *virtual.OpenExistingOptions, requested virtual.AttributesMask, openedFileAttributes *virtual.Attributes) (virtual.Leaf, virtual.AttributesMask, virtual.ChangeInfo, virtual.Status) {
	return virtual.ReadOnlyDirectoryOpenChildDoesntExist(createAttributes)
}

func (WorkerTopLevelDirectory) VirtualReadDir(ctx context.Context, firstCookie uint64, requested virtual.AttributesMask, reporter virtual.DirectoryEntryReporter) virtual.Status {
	return virtual.StatusErrAccess
}

func (WorkerTopLevelDirectory) VirtualApply(data any) bool {
	return false
}

func (WorkerTopLevelDirectory) VirtualOpenNamedAttributes(ctx context.Context, createDirectory bool, requested virtual.AttributesMask, attributes *virtual.Attributes) (virtual.Directory, virtual.Status) {
	if createDirectory {
		return nil, virtual.StatusErrAccess
	}
	return nil, virtual.StatusErrNoEnt
}
