package virtual

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"

	"bonanza.build/pkg/encoding/varint"
	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ObjectBackedDirectoryFactory struct {
	handleAllocator                             virtual.ResolvableHandleAllocator
	directoryClusterReader                      model_parser.MessageObjectReader[object.LocalReference, model_filesystem.DirectoryCluster]
	directoryClusterDecodingParametersSizeBytes int
	leavesReader                                model_parser.MessageObjectReader[object.LocalReference, *model_filesystem_pb.Leaves]
	fileFactory                                 FileFactory
	errorLogger                                 util.ErrorLogger
}

func NewObjectBackedDirectoryFactory(
	handleAllocation virtual.ResolvableHandleAllocation,
	directoryClusterReader model_parser.MessageObjectReader[object.LocalReference, model_filesystem.DirectoryCluster],
	leavesReader model_parser.MessageObjectReader[object.LocalReference, *model_filesystem_pb.Leaves],
	fileFactory FileFactory,
	errorLogger util.ErrorLogger,
) *ObjectBackedDirectoryFactory {
	df := &ObjectBackedDirectoryFactory{
		directoryClusterReader:                      directoryClusterReader,
		directoryClusterDecodingParametersSizeBytes: directoryClusterReader.GetDecodingParametersSizeBytes(),
		leavesReader: leavesReader,
		fileFactory:  fileFactory,
		errorLogger:  errorLogger,
	}
	df.handleAllocator = handleAllocation.AsResolvableAllocator(df.resolveHandle)
	return df
}

func (df *ObjectBackedDirectoryFactory) resolveHandle(r io.ByteReader) (virtual.DirectoryChild, virtual.Status) {
	// Parse cluster reference.
	referenceFormatValue, err := varint.ReadForward[object_pb.ReferenceFormat_Value](r)
	if err != nil {
		return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
	}
	referenceFormat, err := object.NewReferenceFormat(referenceFormatValue)
	if err != nil {
		return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
	}
	referenceSizeBytes := referenceFormat.GetReferenceSizeBytes()
	rawReference := make([]byte, 0, referenceSizeBytes)
	for i := 0; i < referenceSizeBytes; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
		}
		rawReference = append(rawReference, b)
	}
	localReference, err := referenceFormat.NewLocalReference(rawReference)
	if err != nil {
		return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
	}
	decodingParameters := make([]byte, 0, df.directoryClusterDecodingParametersSizeBytes)
	for i := 0; i < df.directoryClusterDecodingParametersSizeBytes; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
		}
		decodingParameters = append(decodingParameters, b)
	}
	clusterReference, err := model_core.NewDecodable(localReference, decodingParameters)
	if err != nil {
		return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
	}

	directoryIndex, err := varint.ReadForward[uint](r)
	if err != nil {
		return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
	}

	symlinkIndex, err := varint.ReadForward[uint](r)
	if err != nil {
		return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
	}
	if symlinkIndex == 0 {
		// File handle resolves to the directory.
		directoriesCount, err := varint.ReadForward[uint32](r)
		if err != nil {
			return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
		}
		return virtual.DirectoryChild{}.FromDirectory(
			df.LookupDirectory(clusterReference, directoryIndex, directoriesCount),
		), virtual.StatusOK
	}

	// File handle resolves to a symbolic link contained in the directory.
	// TODO: Should virtual.HandleResolver provide a context?
	ctx := context.Background()
	cluster, err := df.directoryClusterReader.ReadParsedObject(ctx, clusterReference)
	if err != nil {
		df.errorLogger.Log(util.StatusWrapf(err, "Failed to fetch directory cluster with reference %s", clusterReference))
		return virtual.DirectoryChild{}, virtual.StatusErrIO
	}
	if directoryIndex > uint(len(cluster.Message)) {
		return virtual.DirectoryChild{}, virtual.StatusErrIO
	}

	leaves, err := model_filesystem.DirectoryGetLeaves(
		ctx,
		df.leavesReader,
		model_core.Nested(cluster, cluster.Message[directoryIndex].Directory),
	)
	if err != nil {
		df.errorLogger.Log(util.StatusWrapf(err, "Failed to fetch leaves of directory cluster with reference %s", clusterReference))
		return virtual.DirectoryChild{}, virtual.StatusErrIO
	}

	symlinks := leaves.Message.Symlinks
	if symlinkIndex > uint(len(symlinks)) {
		return virtual.DirectoryChild{}, virtual.StatusErrIO
	}
	return virtual.DirectoryChild{}.FromLeaf(df.createSymlink(clusterReference, directoryIndex, symlinkIndex, symlinks[symlinkIndex-1].Target)), virtual.StatusOK
}

func (df *ObjectBackedDirectoryFactory) LookupDirectory(clusterReference model_core.Decodable[object.LocalReference], directoryIndex uint, directoriesCount uint32) virtual.Directory {
	handle := varint.AppendForward(nil, clusterReference.Value.GetReferenceFormat().ToProto())
	handle = append(handle, clusterReference.Value.GetRawReference()...)
	handle = append(handle, clusterReference.GetDecodingParameters()...)
	handle = varint.AppendForward(handle, directoryIndex)
	handle = varint.AppendForward(handle, 0)
	handle = varint.AppendForward(handle, directoriesCount)
	return df.handleAllocator.New(bytes.NewBuffer(handle)).
		AsStatelessDirectory(&objectBackedDirectory{
			factory:          df,
			clusterReference: clusterReference,
			directoryIndex:   directoryIndex,
			directoriesCount: directoriesCount,
		})
}

func (df *ObjectBackedDirectoryFactory) createSymlink(clusterReference model_core.Decodable[object.LocalReference], directoryIndex, symlinkIndex uint, target string) virtual.LinkableLeaf {
	handle := varint.AppendForward(nil, clusterReference.Value.GetReferenceFormat().ToProto())
	handle = append(handle, clusterReference.Value.GetRawReference()...)
	handle = append(handle, clusterReference.GetDecodingParameters()...)
	handle = varint.AppendForward(handle, directoryIndex)
	handle = varint.AppendForward(handle, symlinkIndex+1)
	return df.handleAllocator.New(bytes.NewBuffer(handle)).
		AsLinkableLeaf(virtual.BaseSymlinkFactory.LookupSymlink([]byte(target)))
}

type objectBackedDirectory struct {
	virtual.ReadOnlyDirectory

	factory          *ObjectBackedDirectoryFactory
	clusterReference model_core.Decodable[object.LocalReference]
	directoryIndex   uint
	directoriesCount uint32
}

func (d *objectBackedDirectory) lookupFile(fileNode model_core.Message[*model_filesystem_pb.FileNode, object.LocalReference]) (virtual.Leaf, virtual.Status) {
	df := d.factory
	properties := fileNode.Message.Properties
	if properties == nil {
		df.errorLogger.Log(status.Errorf(codes.InvalidArgument, "File %#v does not have any properties", fileNode.Message.Name))
		return nil, virtual.StatusErrIO
	}
	fileContents, err := model_filesystem.NewFileContentsEntryFromProto(model_core.Nested(fileNode, properties.Contents))
	if err != nil {
		df.errorLogger.Log(util.StatusWrapf(err, "Invalid contents for file %#v", fileNode.Message.Name))
		return nil, virtual.StatusErrIO
	}
	f, err := df.fileFactory.LookupFile(
		fileContents,
		properties.IsExecutable,
	)
	if err != nil {
		df.errorLogger.Log(util.StatusWrapf(err, "Failed to look up file %#v", fileNode.Message.Name))
		return nil, virtual.StatusErrIO
	}
	return f, virtual.StatusOK
}

func (d *objectBackedDirectory) VirtualGetAttributes(ctx context.Context, requested virtual.AttributesMask, attributes *virtual.Attributes) {
	attributes.SetChangeID(0)
	attributes.SetFileType(filesystem.FileTypeDirectory)
	attributes.SetHasNamedAttributes(false)
	attributes.SetIsInNamedAttributeDirectory(false)
	attributes.SetLinkCount(virtual.EmptyDirectoryLinkCount + d.directoriesCount)
	attributes.SetPermissions(virtual.PermissionsRead | virtual.PermissionsExecute)
	attributes.SetSizeBytes(uint64(d.clusterReference.Value.GetSizeBytes()))
}

func (d *objectBackedDirectory) getDirectory(ctx context.Context) (model_core.Message[*model_filesystem.Directory, object.LocalReference], virtual.Status) {
	df := d.factory
	cluster, err := df.directoryClusterReader.ReadParsedObject(ctx, d.clusterReference)
	if err != nil {
		df.errorLogger.Log(util.StatusWrapf(err, "Failed to fetch directory cluster with reference %s", d.clusterReference))
		return model_core.Message[*model_filesystem.Directory, object.LocalReference]{}, virtual.StatusErrIO
	}
	if d.directoryIndex >= uint(len(cluster.Message)) {
		df.errorLogger.Log(status.Errorf(codes.InvalidArgument, "Directory index %d is out of range, as directory cluster with reference %s only contains %d directories", d.directoryIndex, len(cluster.Message), d.clusterReference))
		return model_core.Message[*model_filesystem.Directory, object.LocalReference]{}, virtual.StatusErrIO
	}
	return model_core.Nested(cluster, &cluster.Message[d.directoryIndex]), virtual.StatusOK
}

func (d objectBackedDirectory) lookupDirectoryNode(directoryNode model_core.Message[*model_filesystem_pb.DirectoryNode, object.LocalReference], childDirectoryIndex int) (virtual.Directory, virtual.Status) {
	df := d.factory
	switch contents := directoryNode.Message.Directory.GetContents().(type) {
	case *model_filesystem_pb.Directory_ContentsExternal:
		childReference, err := model_core.FlattenDecodableReference(model_core.Nested(directoryNode, contents.ContentsExternal.Reference))
		if err != nil {
			df.errorLogger.Log(util.StatusWrapf(err, "Invalid reference for directory with name %#v in directory %d in directory cluster with reference %s", directoryNode.Message.Name, d.directoryIndex, d.clusterReference))
			return nil, virtual.StatusErrIO
		}
		return df.LookupDirectory(childReference, 0, contents.ContentsExternal.DirectoriesCount), virtual.StatusOK
	case *model_filesystem_pb.Directory_ContentsInline:
		return df.LookupDirectory(d.clusterReference, uint(childDirectoryIndex), uint32(len(contents.ContentsInline.Directories))), virtual.StatusOK
	default:
		df.errorLogger.Log(status.Errorf(codes.InvalidArgument, "Invalid contents for directory with name %#v in directory %d in directory cluster with reference %s", directoryNode.Message.Name, d.directoryIndex, d.clusterReference))
		return nil, virtual.StatusErrIO
	}
}

func (d *objectBackedDirectory) VirtualLookup(ctx context.Context, name path.Component, requested virtual.AttributesMask, out *virtual.Attributes) (virtual.DirectoryChild, virtual.Status) {
	df := d.factory
	directory, s := d.getDirectory(ctx)
	if s != virtual.StatusOK {
		return virtual.DirectoryChild{}, s
	}

	// The filesystem model requires that entries stored in a
	// Directory message are sorted alphabetically. Make use of this
	// fact by performing binary searching when looking up entries.
	n := name.String()
	directories := directory.Message.Directory.Directories
	if i, ok := sort.Find(
		len(directories),
		func(i int) int { return strings.Compare(n, directories[i].Name) },
	); ok {
		child, s := d.lookupDirectoryNode(model_core.Nested(directory, directories[i]), directory.Message.ChildDirectoryIndices[i])
		if s != virtual.StatusOK {
			return virtual.DirectoryChild{}, s
		}
		child.VirtualGetAttributes(ctx, requested, out)
		return virtual.DirectoryChild{}.FromDirectory(child), virtual.StatusOK
	}

	leaves, err := model_filesystem.DirectoryGetLeaves(ctx, df.leavesReader, model_core.Nested(directory, directory.Message.Directory))
	if err != nil {
		df.errorLogger.Log(util.StatusWrapf(err, "Failed to fetch leaves of directory cluster with reference %s", d.clusterReference))
		return virtual.DirectoryChild{}, virtual.StatusErrIO
	}

	files := leaves.Message.Files
	if i, ok := sort.Find(
		len(files),
		func(i int) int { return strings.Compare(n, files[i].Name) },
	); ok {
		entry := files[i]
		child, s := d.lookupFile(model_core.Nested(leaves, entry))
		if s != virtual.StatusOK {
			return virtual.DirectoryChild{}, virtual.StatusErrIO
		}
		child.VirtualGetAttributes(ctx, requested, out)
		return virtual.DirectoryChild{}.FromLeaf(child), virtual.StatusOK
	}

	symlinks := leaves.Message.Symlinks
	if i, ok := sort.Find(
		len(symlinks),
		func(i int) int { return strings.Compare(n, symlinks[i].Name) },
	); ok {
		f := df.createSymlink(d.clusterReference, d.directoryIndex, uint(i), symlinks[i].Target)
		f.VirtualGetAttributes(ctx, requested, out)
		return virtual.DirectoryChild{}.FromLeaf(f), virtual.StatusOK
	}

	return virtual.DirectoryChild{}, virtual.StatusErrNoEnt
}

func (d *objectBackedDirectory) VirtualOpenChild(ctx context.Context, name path.Component, shareAccess virtual.ShareMask, createAttributes *virtual.Attributes, existingOptions *virtual.OpenExistingOptions, requested virtual.AttributesMask, openedFileAttributes *virtual.Attributes) (virtual.Leaf, virtual.AttributesMask, virtual.ChangeInfo, virtual.Status) {
	df := d.factory
	directory, s := d.getDirectory(ctx)
	if s != virtual.StatusOK {
		return nil, 0, virtual.ChangeInfo{}, s
	}

	n := name.String()
	directories := directory.Message.Directory.Directories
	if _, ok := sort.Find(
		len(directories),
		func(i int) int { return strings.Compare(n, directories[i].Name) },
	); ok {
		return virtual.ReadOnlyDirectoryOpenChildWrongFileType(existingOptions, virtual.StatusErrIsDir)
	}

	leaves, err := model_filesystem.DirectoryGetLeaves(
		ctx,
		df.leavesReader,
		model_core.Nested(directory, directory.Message.Directory),
	)
	if err != nil {
		df.errorLogger.Log(util.StatusWrapf(err, "Failed to fetch leaves of directory cluster with reference %s", d.clusterReference))
		return nil, 0, virtual.ChangeInfo{}, virtual.StatusErrIO
	}

	files := leaves.Message.Files
	if i, ok := sort.Find(
		len(files),
		func(i int) int { return strings.Compare(n, files[i].Name) },
	); ok {
		if existingOptions == nil {
			return nil, 0, virtual.ChangeInfo{}, virtual.StatusErrExist
		}

		leaf, s := d.lookupFile(model_core.Nested(leaves, files[i]))
		if s != virtual.StatusOK {
			return nil, 0, virtual.ChangeInfo{}, s
		}
		s = leaf.VirtualOpenSelf(ctx, shareAccess, existingOptions, requested, openedFileAttributes)
		return leaf, existingOptions.ToAttributesMask(), virtual.ChangeInfo{}, s
	}

	symlinks := leaves.Message.Symlinks
	if _, ok := sort.Find(
		len(symlinks),
		func(i int) int { return strings.Compare(n, symlinks[i].Name) },
	); ok {
		return virtual.ReadOnlyDirectoryOpenChildWrongFileType(existingOptions, virtual.StatusErrSymlink)
	}

	return virtual.ReadOnlyDirectoryOpenChildDoesntExist(createAttributes)
}

func (d *objectBackedDirectory) VirtualReadDir(ctx context.Context, firstCookie uint64, requested virtual.AttributesMask, reporter virtual.DirectoryEntryReporter) virtual.Status {
	df := d.factory
	directory, s := d.getDirectory(ctx)
	if s != virtual.StatusOK {
		return s
	}

	i := firstCookie
	nextCookieOffset := uint64(1)

	directories := directory.Message.Directory.Directories
	for ; i < uint64(len(directories)); i++ {
		entry := directories[i]
		name, ok := path.NewComponent(entry.Name)
		if !ok {
			df.errorLogger.Log(status.Errorf(codes.InvalidArgument, "Directory %#v has an invalid name", entry.Name))
			return virtual.StatusErrIO
		}
		child, s := d.lookupDirectoryNode(model_core.Nested(directory, entry), directory.Message.ChildDirectoryIndices[i])
		if s != virtual.StatusOK {
			return s
		}
		var attributes virtual.Attributes
		child.VirtualGetAttributes(ctx, requested, &attributes)
		if !reporter.ReportEntry(nextCookieOffset+i, name, virtual.DirectoryChild{}.FromDirectory(child), &attributes) {
			return virtual.StatusOK
		}
	}
	i -= uint64(len(directories))
	nextCookieOffset += uint64(len(directories))

	leaves, err := model_filesystem.DirectoryGetLeaves(
		ctx,
		df.leavesReader,
		model_core.Nested(directory, directory.Message.Directory),
	)
	if err != nil {
		df.errorLogger.Log(util.StatusWrapf(err, "Failed to fetch leaves of directory cluster with reference %s", d.clusterReference))
		return virtual.StatusErrIO
	}

	files := leaves.Message.Files
	for ; i < uint64(len(files)); i++ {
		entry := files[i]
		name, ok := path.NewComponent(entry.Name)
		if !ok {
			df.errorLogger.Log(status.Errorf(codes.InvalidArgument, "File %#v has an invalid name", entry.Name))
			return virtual.StatusErrIO
		}
		child, s := d.lookupFile(model_core.Nested(leaves, entry))
		if s != virtual.StatusOK {
			return s
		}
		var attributes virtual.Attributes
		child.VirtualGetAttributes(ctx, requested, &attributes)
		if !reporter.ReportEntry(nextCookieOffset+i, name, virtual.DirectoryChild{}.FromLeaf(child), &attributes) {
			return virtual.StatusOK
		}
	}
	i -= uint64(len(files))
	nextCookieOffset += uint64(len(files))

	symlinks := leaves.Message.Symlinks
	for ; i < uint64(len(symlinks)); i++ {
		entry := symlinks[i]
		name, ok := path.NewComponent(entry.Name)
		if !ok {
			df.errorLogger.Log(status.Errorf(codes.InvalidArgument, "Symbolic link %#v has an invalid name", entry.Name))
			return virtual.StatusErrIO
		}
		child := df.createSymlink(d.clusterReference, d.directoryIndex, uint(i), entry.Target)
		var attributes virtual.Attributes
		child.VirtualGetAttributes(ctx, requested, &attributes)
		if !reporter.ReportEntry(nextCookieOffset+i, name, virtual.DirectoryChild{}.FromLeaf(child), &attributes) {
			return virtual.StatusOK
		}
	}

	return virtual.StatusOK
}

func (objectBackedDirectory) VirtualApply(data any) bool {
	return false
}

func (objectBackedDirectory) VirtualOpenNamedAttributes(ctx context.Context, createDirectory bool, requested virtual.AttributesMask, attributes *virtual.Attributes) (virtual.Directory, virtual.Status) {
	if createDirectory {
		return nil, virtual.StatusErrAccess
	}
	return nil, virtual.StatusErrNoEnt
}
