package analysis

import (
	"bufio"
	"bytes"
	"context"
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"bonanza.build/pkg/diff"
	"bonanza.build/pkg/label"
	model_command "bonanza.build/pkg/model/command"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/search"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/pool"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/kballard/go-shellquote"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type jsonOrderedMapEntry[T any] struct {
	key   string
	value T
}

// jsonOrderedMap can be used instead of map[string]T to unmarshal JSON
// objects where the original order of fields is preserved.
//
// This is necessary to unmarshal patches declared in source.json files
// that are served by Bazel Central Registry (BCR), as those need to be
// applied in the same order as they are listed in the JSON object.
//
// More details: https://github.com/bazelbuild/bazel/issues/25369
type jsonOrderedMap[T any] []jsonOrderedMapEntry[T]

func (m *jsonOrderedMap[T]) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	t, err := decoder.Token()
	if err != nil {
		return err
	}
	if t != json.Delim('{') {
		return errors.New("expected start of ordered map")
	}
	for {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		if keyToken == json.Delim('}') {
			return nil
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("unexpected token %s", keyToken)
		}

		var value T
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		*m = append(*m, jsonOrderedMapEntry[T]{
			key:   key,
			value: value,
		})
	}
}

// sourceJSON corresponds to the format of source.json files that are
// served by Bazel Central Registry (BCR).
type sourceJSON struct {
	Integrity   string                 `json:"integrity"`
	PatchStrip  int                    `json:"patch_strip"`
	Patches     jsonOrderedMap[string] `json:"patches"`
	StripPrefix string                 `json:"strip_prefix"`
	URL         string                 `json:"url"`
}

type changeTrackingDirectory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	// If set, the directory has not been accessed, and its contents
	// are still identical to the original version. If not set, the
	// directory has been accessed and potentially modified,
	// requiring it to be recomputed and uploaded once again.
	unmodifiedDirectory model_core.Message[*model_filesystem_pb.Directory, TReference]

	directories map[path.Component]*changeTrackingDirectory[TReference, TMetadata]
	files       map[path.Component]*changeTrackingFile[TReference, TMetadata]
	symlinks    map[path.Component]path.Parser
}

func (d *changeTrackingDirectory[TReference, TMetadata]) maximumSymlinkEscapementLevelsAtMost(maximumEscapementLevels uint32) bool {
	if directory := d.unmodifiedDirectory; directory.IsSet() {
		if contentsExternal, ok := directory.Message.GetContents().(*model_filesystem_pb.Directory_ContentsExternal); ok {
			currentMaximumEscapementLevels := contentsExternal.ContentsExternal.MaximumSymlinkEscapementLevels
			if currentMaximumEscapementLevels != nil && currentMaximumEscapementLevels.Value <= maximumEscapementLevels {
				return true
			}
		}
	}
	return false
}

// repoRuleStableInputRootPathUUID is a randomly generated UUID that is
// sent to workers for all actions that belong to repository rules and
// module extensions. This causes the working directory path of these
// actions to be stable.
const repoRuleStableInputRootPathUUID = "c6add83b-eeae-4755-9dde-68ad80fed342"

func (d *changeTrackingDirectory[TReference, TMetadata]) setContents(contents model_core.Message[*model_filesystem_pb.DirectoryContents, TReference], options *changeTrackingDirectoryLoadOptions[TReference]) error {
	leaves, err := model_filesystem.DirectoryGetLeaves(options.context, options.leavesReader, contents)
	if err != nil {
		return err
	}

	d.files = make(map[path.Component]*changeTrackingFile[TReference, TMetadata], len(leaves.Message.Files))
	for _, file := range leaves.Message.Files {
		name, ok := path.NewComponent(file.Name)
		if !ok {
			return fmt.Errorf("file %#v has an invalid name", file.Name)
		}
		f, err := newChangeTrackingFileFromFileProperties[TReference, TMetadata](model_core.Nested(leaves, file.Properties))
		if err != nil {
			return fmt.Errorf("file %#v: %w", file.Name)
		}
		d.files[name] = f
	}
	d.symlinks = make(map[path.Component]path.Parser, len(leaves.Message.Symlinks))
	for _, symlink := range leaves.Message.Symlinks {
		name, ok := path.NewComponent(symlink.Name)
		if !ok {
			return fmt.Errorf("symbolic link %#v has an invalid name", symlink.Name)
		}
		d.symlinks[name] = path.UNIXFormat.NewParser(symlink.Target)
	}

	d.directories = make(map[path.Component]*changeTrackingDirectory[TReference, TMetadata], len(contents.Message.Directories))
	for _, directory := range contents.Message.Directories {
		name, ok := path.NewComponent(directory.Name)
		if !ok {
			return fmt.Errorf("directory %#v has an invalid name", directory.Name)
		}
		d.setDirectorySimple(name, &changeTrackingDirectory[TReference, TMetadata]{
			unmodifiedDirectory: model_core.Nested(contents, directory.Directory),
		})
	}
	return nil
}

func (d *changeTrackingDirectory[TReference, TMetadata]) mergeDirectoryMessage(directoryMessage model_core.Message[*model_filesystem_pb.Directory, TReference], options *changeTrackingDirectoryLoadOptions[TReference]) error {
	// There is no need to perform any merging if both directories
	// are backed by the same message.
	if !d.unmodifiedDirectory.IsSet() || !model_core.MessagesEqual(d.unmodifiedDirectory, directoryMessage) {
		directoryContents, err := model_filesystem.DirectoryGetContents(options.context, options.directoryContentsReader, directoryMessage)
		if err != nil {
			return err
		}
		if err := d.mergeContents(directoryContents, options); err != nil {
			return err
		}
	}
	return nil
}

// mergeContents recursively merges the contents of a given directory
// message into an already existing changeTrackingDirectory. Any
// conflicts will cause merging to fail.
func (d *changeTrackingDirectory[TReference, TMetadata]) mergeContents(contents model_core.Message[*model_filesystem_pb.DirectoryContents, TReference], options *changeTrackingDirectoryLoadOptions[TReference]) error {
	if err := d.maybeLoadContents(options); err != nil {
		return err
	}

	leaves, err := model_filesystem.DirectoryGetLeaves(options.context, options.leavesReader, contents)
	if err != nil {
		return err
	}

	for _, file := range leaves.Message.Files {
		name, ok := path.NewComponent(file.Name)
		if !ok {
			return fmt.Errorf("file %#v has an invalid name", file.Name)
		}

		if _, ok := d.files[name]; ok {
			// TODO: Check equality!
		} else if _, ok := d.symlinks[name]; ok {
			return fmt.Errorf("file %#v conflicts with an existing symbolic link", name.String())
		} else if _, ok := d.directories[name]; ok {
			return fmt.Errorf("file %#v conflicts with an existing directory", name.String())
		} else {
			f, err := newChangeTrackingFileFromFileProperties[TReference, TMetadata](model_core.Nested(leaves, file.Properties))
			if err != nil {
				return fmt.Errorf("file %#v: %w", file.Name)
			}
			d.setFileSimple(name, f)
		}
	}
	for _, symlink := range leaves.Message.Symlinks {
		name, ok := path.NewComponent(symlink.Name)
		if !ok {
			return fmt.Errorf("symbolic link %#v has an invalid name", symlink.Name)
		}

		if _, ok := d.files[name]; ok {
			return fmt.Errorf("symbolic link %#v conflicts with an existing file", name.String())
		} else if _, ok := d.symlinks[name]; ok {
			// TODO: Check equality!
		} else if _, ok := d.directories[name]; ok {
			return fmt.Errorf("symbolic link %#v conflicts with an existing directory", name.String())
		} else {
			d.setSymlinkSimple(name, path.UNIXFormat.NewParser(symlink.Target))
		}
	}

	for _, directory := range contents.Message.Directories {
		name, ok := path.NewComponent(directory.Name)
		if !ok {
			return fmt.Errorf("directory %#v has an invalid name", directory.Name)
		}

		newDirectory := model_core.Nested(contents, directory.Directory)
		if _, ok := d.files[name]; ok {
			return fmt.Errorf("directory %#v conflicts with an existing file", name.String())
		} else if _, ok := d.symlinks[name]; ok {
			return fmt.Errorf("directory %#v conflicts with an existing symbolic link", name.String())
		} else if child, ok := d.directories[name]; ok {
			// Recurse into the already existing directory
			// to merge their contents.
			if err := child.mergeDirectoryMessage(newDirectory, options); err != nil {
				return err
			}
		} else {
			d.setDirectorySimple(name, &changeTrackingDirectory[TReference, TMetadata]{
				unmodifiedDirectory: newDirectory,
			})
		}
	}
	return nil
}

func (d *changeTrackingDirectory[TReference, TMetadata]) mergeDirectory(other *changeTrackingDirectory[TReference, TMetadata], options *changeTrackingDirectoryLoadOptions[TReference]) error {
	if other.unmodifiedDirectory.IsSet() {
		return d.mergeDirectoryMessage(other.unmodifiedDirectory, options)
	}

	if err := d.maybeLoadContents(options); err != nil {
		return err
	}
	if err := other.maybeLoadContents(options); err != nil {
		return err
	}

	for name, f := range other.files {
		if _, ok := d.files[name]; ok {
			// TODO: Check equality!
		} else if _, ok := d.symlinks[name]; ok {
			return fmt.Errorf("file %#v conflicts with an existing symbolic link", name.String())
		} else if _, ok := d.directories[name]; ok {
			return fmt.Errorf("file %#v conflicts with an existing directory", name.String())
		} else {
			d.setFileSimple(name, f)
		}
	}
	for name, target := range other.symlinks {
		if _, ok := d.files[name]; ok {
			return fmt.Errorf("symbolic link %#v conflicts with an existing file", name.String())
		} else if _, ok := d.symlinks[name]; ok {
			// TODO: Check equality!
		} else if _, ok := d.directories[name]; ok {
			return fmt.Errorf("symbolic link %#v conflicts with an existing directory", name.String())
		} else {
			d.setSymlinkSimple(name, target)
		}
	}
	for name, otherChild := range other.directories {
		if _, ok := d.files[name]; ok {
			return fmt.Errorf("directory %#v conflicts with an existing file", name.String())
		} else if _, ok := d.symlinks[name]; ok {
			return fmt.Errorf("directory %#v conflicts with an existing symbolic link", name.String())
		} else if child, ok := d.directories[name]; ok {
			// Recurse into the already existing directory
			// to merge their contents.
			if err := child.mergeDirectory(otherChild, options); err != nil {
				return err
			}
		} else {
			d.setDirectorySimple(name, otherChild)
		}
	}
	return nil
}

func (d *changeTrackingDirectory[TReference, TMetadata]) setDirectorySimple(name path.Component, child *changeTrackingDirectory[TReference, TMetadata]) {
	if d.directories == nil {
		d.directories = map[path.Component]*changeTrackingDirectory[TReference, TMetadata]{}
	}
	d.directories[name] = child
}

func (d *changeTrackingDirectory[TReference, TMetadata]) setDirectory(loadOptions *changeTrackingDirectoryLoadOptions[TReference], name path.Component, f *changeTrackingDirectory[TReference, TMetadata]) error {
	if err := d.maybeLoadContents(loadOptions); err != nil {
		return err
	}

	d.setDirectorySimple(name, f)
	delete(d.files, name)
	delete(d.symlinks, name)
	return nil
}

func (d *changeTrackingDirectory[TReference, TMetadata]) getOrCreateDirectory(name path.Component) (*changeTrackingDirectory[TReference, TMetadata], error) {
	dChild, ok := d.directories[name]
	if !ok {
		if _, ok := d.files[name]; ok {
			return nil, errors.New("a file with this name already exists")
		}
		if _, ok := d.symlinks[name]; ok {
			return nil, errors.New("a symbolic link with this name already exists")
		}
		dChild = &changeTrackingDirectory[TReference, TMetadata]{}
		d.setDirectorySimple(name, dChild)
	}
	return dChild, nil
}

func (d *changeTrackingDirectory[TReference, TMetadata]) setFileSimple(name path.Component, f *changeTrackingFile[TReference, TMetadata]) {
	if d.files == nil {
		d.files = map[path.Component]*changeTrackingFile[TReference, TMetadata]{}
	}
	d.files[name] = f
}

func (d *changeTrackingDirectory[TReference, TMetadata]) setFile(loadOptions *changeTrackingDirectoryLoadOptions[TReference], name path.Component, f *changeTrackingFile[TReference, TMetadata]) error {
	if err := d.maybeLoadContents(loadOptions); err != nil {
		return err
	}

	d.setFileSimple(name, f)
	delete(d.directories, name)
	delete(d.symlinks, name)
	return nil
}

func (d *changeTrackingDirectory[TReference, TMetadata]) setSymlinkSimple(name path.Component, target path.Parser) {
	if d.symlinks == nil {
		d.symlinks = map[path.Component]path.Parser{}
	}
	d.symlinks[name] = target
}

func (d *changeTrackingDirectory[TReference, TMetadata]) setSymlink(loadOptions *changeTrackingDirectoryLoadOptions[TReference], name path.Component, target path.Parser) error {
	if err := d.maybeLoadContents(loadOptions); err != nil {
		return err
	}

	d.setSymlinkSimple(name, target)
	delete(d.directories, name)
	delete(d.files, name)
	return nil
}

type changeTrackingDirectoryLoadOptions[TReference any] struct {
	context                 context.Context
	directoryContentsReader model_parser.ParsedObjectReader[model_core.Decodable[TReference], model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]]
	leavesReader            model_parser.ParsedObjectReader[model_core.Decodable[TReference], model_core.Message[*model_filesystem_pb.Leaves, TReference]]
}

func (d *changeTrackingDirectory[TReference, TMetadata]) maybeLoadContents(options *changeTrackingDirectoryLoadOptions[TReference]) error {
	if directory := d.unmodifiedDirectory; directory.IsSet() {
		// Directory has not been accessed before. Load it from
		// storage and ingest its contents.
		contents, err := model_filesystem.DirectoryGetContents(options.context, options.directoryContentsReader, directory)
		if err != nil {
			return err
		}
		d.unmodifiedDirectory.Clear()
		if err := d.setContents(contents, options); err != nil {
			return err
		}
	}
	return nil
}

type changeTrackingFile[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	isExecutable bool
	contents     changeTrackingFileContents[TReference, TMetadata]
}

func newChangeTrackingFileFromFileProperties[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](m model_core.Message[*model_filesystem_pb.FileProperties, TReference]) (*changeTrackingFile[TReference, TMetadata], error) {
	if m.Message == nil {
		return nil, errors.New("file has no properties")
	}
	return &changeTrackingFile[TReference, TMetadata]{
		isExecutable: m.Message.IsExecutable,
		contents: unmodifiedFileContents[TReference, TMetadata]{
			contents: model_core.Nested(m, m.Message.Contents),
		},
	}, nil
}

type changeTrackingFileContents[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	createFileMerkleTree(ctx context.Context, options *capturableChangeTrackingDirectoryOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, TMetadata], error)
	openRead(ctx context.Context, fileReader *model_filesystem.FileReader[TReference], patchedFiles io.ReaderAt) (io.Reader, error)
}

type unmodifiedFileContents[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	contents model_core.Message[*model_filesystem_pb.FileContents, TReference]
}

func (fc unmodifiedFileContents[TReference, TMetadata]) createFileMerkleTree(ctx context.Context, options *capturableChangeTrackingDirectoryOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, TMetadata], error) {
	return model_core.Patch(options.objectCapturer, fc.contents), nil
}

func (fc unmodifiedFileContents[TReference, TMetadata]) openRead(ctx context.Context, fileReader *model_filesystem.FileReader[TReference], patchedFiles io.ReaderAt) (io.Reader, error) {
	entry, err := model_filesystem.NewFileContentsEntryFromProto(fc.contents)
	if err != nil {
		return nil, err
	}
	return fileReader.FileOpenRead(ctx, entry, 0), nil
}

type patchedFileContents[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	offsetBytes int64
	sizeBytes   int64
}

func (fc patchedFileContents[TReference, TMetadata]) createFileMerkleTree(ctx context.Context, options *capturableChangeTrackingDirectoryOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, TMetadata], error) {
	return model_filesystem.CreateFileMerkleTree(
		ctx,
		options.fileCreationParameters,
		io.NewSectionReader(options.patchedFiles, fc.offsetBytes, fc.sizeBytes),
		options.fileMerkleTreeCapturer,
	)
}

func (fc patchedFileContents[TReference, TMetadata]) openRead(ctx context.Context, fileReader *model_filesystem.FileReader[TReference], patchedFiles io.ReaderAt) (io.Reader, error) {
	return io.NewSectionReader(patchedFiles, fc.offsetBytes, fc.sizeBytes), nil
}

type changeTrackingDirectoryResolver[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	loadOptions *changeTrackingDirectoryLoadOptions[TReference]

	stack    util.NonEmptyStack[*changeTrackingDirectory[TReference, TMetadata]]
	gotScope bool
}

func (r *changeTrackingDirectoryResolver[TReference, TMetadata]) OnAbsolute() (path.ComponentWalker, error) {
	r.stack.PopAll()
	r.gotScope = true
	return r, nil
}

func (r *changeTrackingDirectoryResolver[TReference, TMetadata]) OnRelative() (path.ComponentWalker, error) {
	r.gotScope = true
	return r, nil
}

func (r *changeTrackingDirectoryResolver[TReference, TMetadata]) OnDriveLetter(driveLetter rune) (path.ComponentWalker, error) {
	return nil, errors.New("drive letters are not supported")
}

func (r *changeTrackingDirectoryResolver[TReference, TMetadata]) OnShare(server, share string) (path.ComponentWalker, error) {
	return nil, errors.New("shares are not supported")
}

func (r *changeTrackingDirectoryResolver[TReference, TMetadata]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	d := r.stack.Peek()
	if err := d.maybeLoadContents(r.loadOptions); err != nil {
		return nil, err
	}
	if dChild, ok := d.directories[name]; ok {
		r.stack.Push(dChild)
		return path.GotDirectory{
			Child:        r,
			IsReversible: true,
		}, nil
	}
	return nil, errDirectoryDoesNotExist
}

func (r *changeTrackingDirectoryResolver[TReference, TMetadata]) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	return path.OnTerminalViaOnDirectory(r, name)
}

func (r *changeTrackingDirectoryResolver[TReference, TMetadata]) OnUp() (path.ComponentWalker, error) {
	if _, ok := r.stack.PopSingle(); !ok {
		return nil, errors.New("path resolves to a location above the root directory")
	}
	return r, nil
}

type capturableChangeTrackingDirectoryOptions[TReference, TMetadata any] struct {
	context                 context.Context
	directoryContentsReader model_parser.ParsedObjectReader[model_core.Decodable[TReference], model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]]
	leavesReader            model_parser.ParsedObjectReader[model_core.Decodable[TReference], model_core.Message[*model_filesystem_pb.Leaves, TReference]]
	fileCreationParameters  *model_filesystem.FileCreationParameters
	fileMerkleTreeCapturer  model_filesystem.FileMerkleTreeCapturer[TMetadata]
	patchedFiles            io.ReaderAt
	objectCapturer          model_core.ExistingObjectCapturer[TReference, TMetadata]
}

type capturableChangeTrackingDirectory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	options   *capturableChangeTrackingDirectoryOptions[TReference, TMetadata]
	directory *changeTrackingDirectory[TReference, TMetadata]
}

func (cd *capturableChangeTrackingDirectory[TReference, TMetadata]) Close() error {
	return nil
}

func (cd *capturableChangeTrackingDirectory[TReference, TMetadata]) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[TMetadata], model_filesystem.CapturableDirectory[TMetadata, TMetadata], error) {
	dChild, ok := cd.directory.directories[name]
	if !ok {
		panic("attempted to enter non-existent directory")
	}
	if directory := dChild.unmodifiedDirectory; directory.IsSet() {
		// Directory has not been modified. Load the copy from
		// storage, so that it may potentially be inlined into
		// the parent directory.
		contents, err := model_filesystem.DirectoryGetContents(cd.options.context, cd.options.directoryContentsReader, directory)
		if err != nil {
			return nil, nil, err
		}
		// TODO: This ends up recomputing
		// MaximumSymlinkEscapementLevels, which may be readily
		// available if this was a reference. Should we add a
		// utility function to model_filesystem to prevent this?
		patchedContents := model_core.Patch(cd.options.objectCapturer, contents)
		createdDirectory, err := model_filesystem.NewCreatedDirectoryBare(patchedContents)
		return createdDirectory, nil, err
	}

	// Directory contains one or more changes. Recurse into it.
	return nil,
		&capturableChangeTrackingDirectory[TReference, TMetadata]{
			options:   cd.options,
			directory: dChild,
		},
		nil
}

func (cd *capturableChangeTrackingDirectory[TReference, TMetadata]) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[TMetadata], error) {
	file, ok := cd.directory.files[name]
	if !ok {
		panic("attempted to enter non-existent file")
	}
	return &capturableChangeTrackingFile[TReference, TMetadata]{
		options:  cd.options,
		contents: file.contents,
	}, nil
}

func (cd *capturableChangeTrackingDirectory[TReference, TMetadata]) ReadDir() ([]filesystem.FileInfo, error) {
	d := cd.directory
	if err := d.maybeLoadContents(&changeTrackingDirectoryLoadOptions[TReference]{
		context:                 cd.options.context,
		directoryContentsReader: cd.options.directoryContentsReader,
		leavesReader:            cd.options.leavesReader,
	}); err != nil {
		return nil, err
	}

	infos := make(filesystem.FileInfoList, 0, len(d.directories)+len(d.files)+len(d.symlinks))
	for name := range d.directories {
		infos = append(infos, filesystem.NewFileInfo(name, filesystem.FileTypeDirectory, false))
	}
	for name, file := range d.files {
		infos = append(infos, filesystem.NewFileInfo(name, filesystem.FileTypeRegularFile, file.isExecutable))
	}
	for name := range d.symlinks {
		infos = append(infos, filesystem.NewFileInfo(name, filesystem.FileTypeSymlink, false))
	}
	sort.Sort(infos)
	return infos, nil
}

func (cd *capturableChangeTrackingDirectory[TReference, TMetadata]) Readlink(name path.Component) (path.Parser, error) {
	target, ok := cd.directory.symlinks[name]
	if !ok {
		panic("attempted to read non-existent symbolic link")
	}
	return target, nil
}

type capturableChangeTrackingFile[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	options  *capturableChangeTrackingDirectoryOptions[TReference, TMetadata]
	contents changeTrackingFileContents[TReference, TMetadata]
}

func (cf *capturableChangeTrackingFile[TReference, TMetadata]) CreateFileMerkleTree(ctx context.Context) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, TMetadata], error) {
	return cf.contents.createFileMerkleTree(ctx, cf.options)
}

func (cf *capturableChangeTrackingFile[TReference, TMetadata]) Discard() {}

type strippingComponentWalker struct {
	remainder            path.ComponentWalker
	additionalStripCount int
}

func newStrippingComponentWalker(remainder path.ComponentWalker, stripCount int) path.ComponentWalker {
	return strippingComponentWalker{
		remainder:            remainder,
		additionalStripCount: stripCount,
	}.stripComponent()
}

func (cw strippingComponentWalker) stripComponent() path.ComponentWalker {
	if cw.additionalStripCount > 0 {
		return strippingComponentWalker{
			remainder:            cw.remainder,
			additionalStripCount: cw.additionalStripCount - 1,
		}
	}
	return cw.remainder
}

func (cw strippingComponentWalker) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	return path.GotDirectory{
		Child:        cw.stripComponent(),
		IsReversible: false,
	}, nil
}

func (cw strippingComponentWalker) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	return nil, nil
}

func (cw strippingComponentWalker) OnUp() (path.ComponentWalker, error) {
	return cw.stripComponent(), nil
}

type changeTrackingDirectoryExistingFileResolver[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	path.TerminalNameTrackingComponentWalker

	loadOptions *changeTrackingDirectoryLoadOptions[TReference]
	stack       util.NonEmptyStack[*changeTrackingDirectory[TReference, TMetadata]]
	gotScope    bool
}

func (r *changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]) OnAbsolute() (path.ComponentWalker, error) {
	r.stack.PopAll()
	r.gotScope = true
	return r, nil
}

func (r *changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]) OnRelative() (path.ComponentWalker, error) {
	r.gotScope = true
	return r, nil
}

func (r *changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]) OnDriveLetter(driveLetter rune) (path.ComponentWalker, error) {
	return nil, errors.New("drive letters are not supported")
}

func (r *changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]) OnShare(server, share string) (path.ComponentWalker, error) {
	return nil, errors.New("shares are not supported")
}

var errDirectoryDoesNotExist = errors.New("directory does not exist")

func (r *changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	d := r.stack.Peek()
	if err := d.maybeLoadContents(r.loadOptions); err != nil {
		return nil, err
	}
	if dChild, ok := d.directories[name]; ok {
		r.stack.Push(dChild)
		return path.GotDirectory{
			Child:        r,
			IsReversible: true,
		}, nil
	}
	return nil, errDirectoryDoesNotExist
}

func (r *changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]) OnUp() (path.ComponentWalker, error) {
	if _, ok := r.stack.PopSingle(); !ok {
		return nil, errors.New("path resolves to a location above the root directory")
	}
	return r, nil
}

var errFileDoesNotExist = errors.New("file does not exist")

func (r *changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]) getFile() (*changeTrackingFile[TReference, TMetadata], error) {
	if r.TerminalName == nil {
		return nil, errors.New("path does not resolve to a file")
	}
	d := r.stack.Peek()
	if err := d.maybeLoadContents(r.loadOptions); err != nil {
		return nil, err
	}
	if f, ok := d.files[*r.TerminalName]; ok {
		return f, nil
	}
	return nil, errFileDoesNotExist
}

type changeTrackingDirectoryNewDirectoryResolver[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	loadOptions *changeTrackingDirectoryLoadOptions[TReference]
	stack       util.NonEmptyStack[*changeTrackingDirectory[TReference, TMetadata]]
}

func (r *changeTrackingDirectoryNewDirectoryResolver[TReference, TMetadata]) OnAbsolute() (path.ComponentWalker, error) {
	r.stack.PopAll()
	return r, nil
}

func (r *changeTrackingDirectoryNewDirectoryResolver[TReference, TMetadata]) OnRelative() (path.ComponentWalker, error) {
	return r, nil
}

func (r *changeTrackingDirectoryNewDirectoryResolver[TReference, TMetadata]) OnDriveLetter(driveLetter rune) (path.ComponentWalker, error) {
	return nil, errors.New("drive letters are not supported")
}

func (r *changeTrackingDirectoryNewDirectoryResolver[TReference, TMetadata]) OnShare(server, share string) (path.ComponentWalker, error) {
	return nil, errors.New("shares are not supported")
}

func (r *changeTrackingDirectoryNewDirectoryResolver[TReference, TMetadata]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	d := r.stack.Peek()
	if err := d.maybeLoadContents(r.loadOptions); err != nil {
		return nil, err
	}

	dChild, err := d.getOrCreateDirectory(name)
	if err != nil {
		return nil, fmt.Errorf("failed to create directory %#v: %w", name.String(), err)
	}

	r.stack.Push(dChild)
	return path.GotDirectory{
		Child:        r,
		IsReversible: true,
	}, nil
}

func (r *changeTrackingDirectoryNewDirectoryResolver[TReference, TMetadata]) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	return path.OnTerminalViaOnDirectory(r, name)
}

func (r *changeTrackingDirectoryNewDirectoryResolver[TReference, TMetadata]) OnUp() (path.ComponentWalker, error) {
	if _, ok := r.stack.PopSingle(); !ok {
		return nil, errors.New("path resolves to a location above the root directory")
	}
	return r, nil
}

type changeTrackingDirectoryNewFileResolver[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	path.TerminalNameTrackingComponentWalker

	loadOptions *changeTrackingDirectoryLoadOptions[TReference]
	stack       util.NonEmptyStack[*changeTrackingDirectory[TReference, TMetadata]]
}

func (r *changeTrackingDirectoryNewFileResolver[TReference, TMetadata]) OnAbsolute() (path.ComponentWalker, error) {
	r.stack.PopAll()
	return r, nil
}

func (r *changeTrackingDirectoryNewFileResolver[TReference, TMetadata]) OnRelative() (path.ComponentWalker, error) {
	return r, nil
}

func (r *changeTrackingDirectoryNewFileResolver[TReference, TMetadata]) OnDriveLetter(driveLetter rune) (path.ComponentWalker, error) {
	return nil, errors.New("drive letters are not supported")
}

func (r *changeTrackingDirectoryNewFileResolver[TReference, TMetadata]) OnShare(server, share string) (path.ComponentWalker, error) {
	return nil, errors.New("drive letters are not supported")
}

func (r *changeTrackingDirectoryNewFileResolver[TReference, TMetadata]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	d := r.stack.Peek()
	if err := d.maybeLoadContents(r.loadOptions); err != nil {
		return nil, err
	}

	dChild, err := d.getOrCreateDirectory(name)
	if err != nil {
		return nil, fmt.Errorf("failed to create directory %#v: %w", name.String(), err)
	}

	r.stack.Push(dChild)
	return path.GotDirectory{
		Child:        r,
		IsReversible: true,
	}, nil
}

func (r *changeTrackingDirectoryNewFileResolver[TReference, TMetadata]) OnUp() (path.ComponentWalker, error) {
	if _, ok := r.stack.PopSingle(); !ok {
		return nil, errors.New("path resolves to a location above the root directory")
	}
	return r, nil
}

func inferArchiveFormatFromURL(url string) (model_analysis_pb.HttpArchiveContents_Key_Format, bool) {
	if strings.HasSuffix(url, ".tar.gz") {
		return model_analysis_pb.HttpArchiveContents_Key_TAR_GZ, true
	}
	if strings.HasSuffix(url, ".tar.xz") {
		return model_analysis_pb.HttpArchiveContents_Key_TAR_XZ, true
	}
	if strings.HasSuffix(url, ".zip") {
		return model_analysis_pb.HttpArchiveContents_Key_ZIP, true
	}
	return 0, false
}

func parseSubresourceIntegrity(integrity string) (*model_fetch_pb.SubresourceIntegrity, error) {
	dash := strings.IndexByte(integrity, '-')
	if dash < 0 {
		return nil, errors.New("subresource integrity does not contain a dash")
	}

	hashAlgorithmStr := integrity[:dash]
	hashAlgorithm, ok := model_fetch_pb.SubresourceIntegrity_HashAlgorithm_value[strings.ToUpper(hashAlgorithmStr)]
	if !ok {
		return nil, fmt.Errorf("unknown hash algorithm %#v", hashAlgorithmStr)
	}

	hashStr := integrity[dash+1:]
	hash, err := base64.StdEncoding.DecodeString(hashStr)
	if err != nil {
		return nil, fmt.Errorf("invalid hash %#v: %w", hashStr, err)
	}

	return &model_fetch_pb.SubresourceIntegrity{
		HashAlgorithm: model_fetch_pb.SubresourceIntegrity_HashAlgorithm(hashAlgorithm),
		Hash:          hash,
	}, nil
}

func parseSubresourceIntegrityOrSHA256(integrity, sha256 string) (*model_fetch_pb.SubresourceIntegrity, error) {
	if integrity != "" {
		return parseSubresourceIntegrity(integrity)
	}
	if sha256 != "" {
		sha256Bytes, err := hex.DecodeString(sha256)
		if err != nil {
			return nil, fmt.Errorf("invalid sha256: %w", err)
		}
		return &model_fetch_pb.SubresourceIntegrity{
			HashAlgorithm: model_fetch_pb.SubresourceIntegrity_SHA256,
			Hash:          sha256Bytes,
		}, nil
	}
	return nil, nil
}

func (c *baseComputer[TReference, TMetadata]) fetchModuleFromRegistry(
	ctx context.Context,
	module *model_analysis_pb.BuildListModule,
	e RepoEnvironment[TReference, TMetadata],
	singleVersionOverridePatchLabels []string,
	singleVersionOverridePatchCommands []string,
	singleVersionOverridePatchStrip int,
) (PatchedRepoValue[TMetadata], error) {
	fileReader, gotFileReader := e.GetFileReaderValue(&model_analysis_pb.FileReader_Key{})
	if !gotFileReader {
		return PatchedRepoValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	sourceJSONURL, err := url.JoinPath(
		module.RegistryUrl,
		"modules",
		module.Name,
		module.Version,
		"source.json",
	)
	if err != nil {
		return PatchedRepoValue[TMetadata]{}, fmt.Errorf("failed to construct URL for module %s with version %s in registry %#v: %w", module.Name, module.Version, module.RegistryUrl, err)
	}

	sourceJSONContentsValue := e.GetHttpFileContentsValue(
		&model_analysis_pb.HttpFileContents_Key{
			FetchOptions: &model_analysis_pb.HttpFetchOptions{
				Target: &model_fetch_pb.Target{
					Urls: []string{sourceJSONURL},
				},
			},
		},
	)
	if !sourceJSONContentsValue.IsSet() {
		return PatchedRepoValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	if sourceJSONContentsValue.Message.Exists == nil {
		return PatchedRepoValue[TMetadata]{}, fmt.Errorf("file at URL %#v does not exist", sourceJSONURL)
	}
	sourceJSONContentsEntry, err := model_filesystem.NewFileContentsEntryFromProto(
		model_core.Nested(sourceJSONContentsValue, sourceJSONContentsValue.Message.Exists.Contents),
	)
	if err != nil {
		return PatchedRepoValue[TMetadata]{}, fmt.Errorf("invalid file contents: %w", err)
	}

	sourceJSONData, err := fileReader.FileReadAll(ctx, sourceJSONContentsEntry, 1<<20)
	if err != nil {
		return PatchedRepoValue[TMetadata]{}, err
	}
	var sourceJSON sourceJSON
	if err := json.Unmarshal(sourceJSONData, &sourceJSON); err != nil {
		return PatchedRepoValue[TMetadata]{}, fmt.Errorf("invalid JSON contents for %#v: %w", sourceJSONURL, err)
	}

	archiveFormat, ok := inferArchiveFormatFromURL(sourceJSON.URL)
	if !ok {
		return PatchedRepoValue[TMetadata]{}, fmt.Errorf("cannot derive archive format from file extension of URL %#v", sourceJSONURL)
	}

	integrity, err := parseSubresourceIntegrity(sourceJSON.Integrity)
	if err != nil {
		return PatchedRepoValue[TMetadata]{}, fmt.Errorf("invalid subresource integrity %#v in %#v: %w", sourceJSON.Integrity, sourceJSONURL, err)
	}

	// Download source archive and all patches that need to be
	// applied. Return all the errors at once - this way, anything
	// that needs downloading is done.
	missingDependencies := false
	archiveContentsValue := e.GetHttpArchiveContentsValue(&model_analysis_pb.HttpArchiveContents_Key{
		FetchOptions: &model_analysis_pb.HttpFetchOptions{
			Target: &model_fetch_pb.Target{
				Urls:      []string{sourceJSON.URL},
				Integrity: integrity,
			},
		},
		Format: archiveFormat,
	})
	if !archiveContentsValue.IsSet() {
		missingDependencies = true
	}

	// Process patches stored in source.json.
	patchesToApply := make([]patchToApply[TReference], 0, len(sourceJSON.Patches)+len(singleVersionOverridePatchLabels))
	for _, patchEntry := range sourceJSON.Patches {
		patchURL, err := url.JoinPath(
			module.RegistryUrl,
			"modules",
			module.Name,
			module.Version,
			"patches",
			patchEntry.key,
		)
		if err != nil {
			return PatchedRepoValue[TMetadata]{}, fmt.Errorf("failed to construct URL for patch %s of module %s with version %s in registry %#v: %w", patchEntry.key, module.Name, module.Version, module.RegistryUrl, err)
		}

		integrity, err := parseSubresourceIntegrity(patchEntry.value)
		if err != nil {
			return PatchedRepoValue[TMetadata]{}, fmt.Errorf("invalid subresource integrity %#v for patch %#v: %w", patchEntry.value, patchURL, err)
		}

		patchContentsValue := e.GetHttpFileContentsValue(&model_analysis_pb.HttpFileContents_Key{
			FetchOptions: &model_analysis_pb.HttpFetchOptions{
				Target: &model_fetch_pb.Target{
					Urls:      []string{patchURL},
					Integrity: integrity,
				},
			},
		})
		if !patchContentsValue.IsSet() {
			missingDependencies = true
			continue
		}
		if patchContentsValue.Message.Exists == nil {
			return PatchedRepoValue[TMetadata]{}, fmt.Errorf("patch at URL %#v does not exist", patchEntry.key)
		}

		patchContentsEntry, err := model_filesystem.NewFileContentsEntryFromProto(
			model_core.Nested(patchContentsValue, patchContentsValue.Message.Exists.Contents),
		)
		if err != nil {
			return PatchedRepoValue[TMetadata]{}, fmt.Errorf("invalid file contents for patch %#v: %w", patchEntry.key, err)
		}

		patchesToApply = append(patchesToApply, patchToApply[TReference]{
			filename:          patchEntry.key,
			strip:             sourceJSON.PatchStrip,
			fileContentsEntry: patchContentsEntry,
		})
	}

	// If a single_version_override() is present, we may need to
	// apply additional patches.
	for _, patchLabelStr := range singleVersionOverridePatchLabels {
		patchLabel, err := label.NewCanonicalLabel(patchLabelStr)
		if err != nil {
			return PatchedRepoValue[TMetadata]{}, fmt.Errorf("invalid single version override patch label %#v: %w", patchLabelStr)
		}
		patchPropertiesValue := e.GetFilePropertiesValue(&model_analysis_pb.FileProperties_Key{
			CanonicalRepo: patchLabel.GetCanonicalPackage().GetCanonicalRepo().String(),
			Path:          patchLabel.GetRepoRelativePath(),
		})
		if !patchPropertiesValue.IsSet() {
			missingDependencies = true
			continue
		}
		if patchPropertiesValue.Message.Exists == nil {
			return PatchedRepoValue[TMetadata]{}, fmt.Errorf("patch %#v does not exist", patchLabelStr)
		}

		patchContentsEntry, err := model_filesystem.NewFileContentsEntryFromProto(
			model_core.Nested(patchPropertiesValue, patchPropertiesValue.Message.Exists.Contents),
		)
		if err != nil {
			return PatchedRepoValue[TMetadata]{}, fmt.Errorf("invalid file contents for patch %#v: %s", patchLabelStr, err)
		}

		patchesToApply = append(patchesToApply, patchToApply[TReference]{
			filename:          patchLabelStr,
			strip:             singleVersionOverridePatchStrip,
			fileContentsEntry: patchContentsEntry,
		})
	}

	if missingDependencies {
		return PatchedRepoValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	if archiveContentsValue.Message.Exists == nil {
		return PatchedRepoValue[TMetadata]{}, fmt.Errorf("file at URL %#v does not exist", sourceJSON.URL)
	}

	// TODO: Process singleVersionOverridePatchCommands!

	return c.applyPatches(
		ctx,
		e,
		model_core.Nested(archiveContentsValue, archiveContentsValue.Message.Exists.Contents),
		sourceJSON.StripPrefix,
		patchesToApply,
	)
}

type patchToApply[TReference any] struct {
	filename          string
	strip             int
	fileContentsEntry model_filesystem.FileContentsEntry[TReference]
}

type applyPatchesEnvironment[TReference object.BasicReference, TMetadata any] interface {
	model_core.ObjectCapturer[TReference, TMetadata]

	GetDirectoryCreationParametersObjectValue(key *model_analysis_pb.DirectoryCreationParametersObject_Key) (*model_filesystem.DirectoryCreationParameters, bool)
	GetDirectoryReadersValue(key *model_analysis_pb.DirectoryReaders_Key) (*DirectoryReaders[TReference], bool)
	GetFileCreationParametersObjectValue(key *model_analysis_pb.FileCreationParametersObject_Key) (*model_filesystem.FileCreationParameters, bool)
	GetFileReaderValue(key *model_analysis_pb.FileReader_Key) (*model_filesystem.FileReader[TReference], bool)
}

func (c *baseComputer[TReference, TMetadata]) applyPatches(
	ctx context.Context,
	e applyPatchesEnvironment[TReference, TMetadata],
	rootRef model_core.Message[*model_filesystem_pb.DirectoryReference, TReference],
	stripPrefix string,
	patches []patchToApply[TReference],
) (PatchedRepoValue[TMetadata], error) {
	fileReader, gotFileReader := e.GetFileReaderValue(&model_analysis_pb.FileReader_Key{})
	directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObjectValue(&model_analysis_pb.DirectoryCreationParametersObject_Key{})
	directoryReaders, gotDirectoryReaders := e.GetDirectoryReadersValue(&model_analysis_pb.DirectoryReaders_Key{})
	fileCreationParameters, gotFileCreationParameters := e.GetFileCreationParametersObjectValue(&model_analysis_pb.FileCreationParametersObject_Key{})
	if !gotFileReader || !gotDirectoryCreationParameters || !gotDirectoryReaders || !gotFileCreationParameters {
		return PatchedRepoValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	rootDirectory := &changeTrackingDirectory[TReference, TMetadata]{
		unmodifiedDirectory: model_core.Nested(
			rootRef,
			&model_filesystem_pb.Directory{
				Contents: &model_filesystem_pb.Directory_ContentsExternal{
					ContentsExternal: rootRef.Message,
				},
			},
		),
	}

	// Strip the provided directory prefix.
	loadOptions := &changeTrackingDirectoryLoadOptions[TReference]{
		context:                 ctx,
		directoryContentsReader: directoryReaders.DirectoryContents,
		leavesReader:            directoryReaders.Leaves,
	}
	rootDirectoryResolver := changeTrackingDirectoryResolver[TReference, TMetadata]{
		loadOptions: loadOptions,
		stack:       util.NewNonEmptyStack(rootDirectory),
	}
	if err := path.Resolve(
		path.UNIXFormat.NewParser(stripPrefix),
		path.NewRelativeScopeWalker(&rootDirectoryResolver),
	); err != nil {
		return PatchedRepoValue[TMetadata]{}, fmt.Errorf("failed to strip prefix %#v from contents: %w", stripPrefix, err)
	}
	rootDirectory = rootDirectoryResolver.stack.Peek()

	patchedFiles, err := c.filePool.NewFile(pool.ZeroHoleSource, 0)
	if err != nil {
		return PatchedRepoValue[TMetadata]{}, err
	}
	defer patchedFiles.Close()
	patchedFilesWriter := model_filesystem.NewSectionWriter(patchedFiles)

	for _, patch := range patches {
		err = c.applyPatch(
			ctx,
			rootDirectory,
			loadOptions,
			patch.strip,
			fileReader,
			func() (io.Reader, error) {
				return fileReader.FileOpenRead(ctx, patch.fileContentsEntry, 0), nil
			},
			patchedFiles,
			patchedFilesWriter,
		)
		if err != nil {
			return PatchedRepoValue[TMetadata]{}, fmt.Errorf("patch %q: %w", patch.filename, err)
		}
	}

	return c.returnRepoMerkleTree(
		ctx,
		e,
		rootDirectory,
		directoryCreationParameters,
		directoryReaders,
		fileCreationParameters,
		patchedFiles,
	)
}

func (c *baseComputer[TReference, TMetadata]) applyPatch(
	ctx context.Context,
	rootDirectory *changeTrackingDirectory[TReference, TMetadata],
	loadOptions *changeTrackingDirectoryLoadOptions[TReference],
	patchStrip int,
	fileReader *model_filesystem.FileReader[TReference],
	openFile func() (io.Reader, error),
	patchedFiles filesystem.FileReader,
	patchedFilesWriter *model_filesystem.SectionWriter,
) error {
	patchedFile, err := openFile()
	if err != nil {
		return fmt.Errorf("open patched file contents: %s", err)
	}

	files, _, err := gitdiff.Parse(patchedFile)
	if err != nil {
		return fmt.Errorf("invalid patch: %w", err)
	}

	for _, file := range files {
		var fileContents changeTrackingFileContents[TReference, TMetadata]
		isExecutable := false
		if !file.IsNew {
			r := &changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]{
				loadOptions: loadOptions,
				stack:       util.NewNonEmptyStack(rootDirectory),
			}
			if err := path.Resolve(
				path.UNIXFormat.NewParser(file.OldName),
				path.NewRelativeScopeWalker(
					newStrippingComponentWalker(r, patchStrip),
				),
			); err != nil {
				return fmt.Errorf("cannot resolve path %#v: %w", file.OldName, err)
			}
			f, err := r.getFile()
			if err != nil {
				// go-gitdiff only considers files new if the
				// old name is /dev/null. However, there are
				// also many patches out there that don't do
				// that. Treat non-existent files as if they
				// are empty.
				if !errors.Is(err, errFileDoesNotExist) {
					return fmt.Errorf("cannot get file at path %#v: %w", file.OldName, err)
				}
			} else {
				fileContents = f.contents
				isExecutable = f.isExecutable
			}
		}

		// Compute the offsets at which changes need to
		// be made to the file.
		var srcScan io.Reader
		if fileContents == nil {
			srcScan = bytes.NewBuffer(nil)
		} else {
			srcScan, err = fileContents.openRead(ctx, fileReader, patchedFiles)
			if err != nil {
				return fmt.Errorf("failed to open file %#v: %s", file.OldName, err)
			}
		}
		fragmentsOffsetsBytes, err := diff.FindTextFragmentOffsetsBytes(file.TextFragments, bufio.NewReader(srcScan))
		if err != nil {
			return fmt.Errorf("failed to apply to file %#v: %w", file.OldName, err)
		}

		var srcReplace io.Reader
		if fileContents == nil {
			srcReplace = bytes.NewBuffer(nil)
		} else {
			srcReplace, err = fileContents.openRead(ctx, fileReader, patchedFiles)
			if err != nil {
				return fmt.Errorf("failed to open file %#v: %w", file.OldName, err)
			}
		}

		patchedFileOffsetBytes := patchedFilesWriter.GetOffsetBytes()
		if err := diff.ReplaceTextFragments(patchedFilesWriter, srcReplace, file.TextFragments, fragmentsOffsetsBytes); err != nil {
			return fmt.Errorf("failed to replace text fragments to %#v: %w", file.OldName, err)
		}

		r := &changeTrackingDirectoryNewFileResolver[TReference, TMetadata]{
			loadOptions: loadOptions,
			stack:       util.NewNonEmptyStack(rootDirectory),
		}
		if err := path.Resolve(
			path.UNIXFormat.NewParser(file.NewName),
			path.NewRelativeScopeWalker(
				newStrippingComponentWalker(r, patchStrip),
			),
		); err != nil {
			return fmt.Errorf("cannot resolve path %#v: %w", file.NewName, err)
		}
		if r.TerminalName == nil {
			return fmt.Errorf("path %#v does not resolve to a file", file.NewName)
		}

		if file.NewMode != 0 {
			isExecutable = file.NewMode&0o111 != 0
		}
		if err := r.stack.Peek().setFile(
			loadOptions,
			*r.TerminalName,
			&changeTrackingFile[TReference, TMetadata]{
				isExecutable: isExecutable,
				contents: patchedFileContents[TReference, TMetadata]{
					offsetBytes: patchedFileOffsetBytes,
					sizeBytes:   patchedFilesWriter.GetOffsetBytes() - patchedFileOffsetBytes,
				},
			},
		); err != nil {
			return err
		}
	}
	return nil
}

// newRepositoryOS creates a repository_os object that can be embedded
// into module_ctx and repository_ctx objects.
func newRepositoryOS[TReference object.BasicReference, TMetadata BaseComputerReferenceMetadata](thread *starlark.Thread, repoPlatform *model_analysis_pb.RegisteredRepoPlatform_Value) starlark.Value {
	environ := starlark.NewDict(len(repoPlatform.RepositoryOsEnviron))
	for _, entry := range repoPlatform.RepositoryOsEnviron {
		environ.SetKey(thread, starlark.String(entry.Name), starlark.String(entry.Value))
	}
	s := model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
		"arch":    starlark.String(repoPlatform.RepositoryOsArch),
		"environ": environ,
		"name":    starlark.String(repoPlatform.RepositoryOsName),
	})
	s.Freeze()
	return s
}

type moduleOrRepositoryContextEnvironment[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ObjectCapturer[TReference, TMetadata]

	GetActionEncoderObjectValue(*model_analysis_pb.ActionEncoderObject_Key) (model_encoding.BinaryEncoder, bool)
	GetActionResultValue(model_core.PatchedMessage[*model_analysis_pb.ActionResult_Key, TMetadata]) model_core.Message[*model_analysis_pb.ActionResult_Value, TReference]
	GetDirectoryCreationParametersObjectValue(*model_analysis_pb.DirectoryCreationParametersObject_Key) (*model_filesystem.DirectoryCreationParameters, bool)
	GetDirectoryCreationParametersValue(*model_analysis_pb.DirectoryCreationParameters_Key) model_core.Message[*model_analysis_pb.DirectoryCreationParameters_Value, TReference]
	GetDirectoryReadersValue(key *model_analysis_pb.DirectoryReaders_Key) (*DirectoryReaders[TReference], bool)
	GetFileCreationParametersObjectValue(*model_analysis_pb.FileCreationParametersObject_Key) (*model_filesystem.FileCreationParameters, bool)
	GetFileCreationParametersValue(*model_analysis_pb.FileCreationParameters_Key) model_core.Message[*model_analysis_pb.FileCreationParameters_Value, TReference]
	GetFileReaderValue(*model_analysis_pb.FileReader_Key) (*model_filesystem.FileReader[TReference], bool)
	GetHttpArchiveContentsValue(*model_analysis_pb.HttpArchiveContents_Key) model_core.Message[*model_analysis_pb.HttpArchiveContents_Value, TReference]
	GetHttpFileContentsValue(*model_analysis_pb.HttpFileContents_Key) model_core.Message[*model_analysis_pb.HttpFileContents_Value, TReference]
	GetRegisteredRepoPlatformValue(*model_analysis_pb.RegisteredRepoPlatform_Key) model_core.Message[*model_analysis_pb.RegisteredRepoPlatform_Value, TReference]
	GetRepoPlatformHostPathValue(*model_analysis_pb.RepoPlatformHostPath_Key) model_core.Message[*model_analysis_pb.RepoPlatformHostPath_Value, TReference]
	GetRepoValue(*model_analysis_pb.Repo_Key) model_core.Message[*model_analysis_pb.Repo_Value, TReference]
	GetRootModuleValue(*model_analysis_pb.RootModule_Key) model_core.Message[*model_analysis_pb.RootModule_Value, TReference]
	GetStableInputRootPathObjectValue(*model_analysis_pb.StableInputRootPathObject_Key) (*model_starlark.BarePath, bool)
	GetSuccessfulActionResultValue(model_core.PatchedMessage[*model_analysis_pb.SuccessfulActionResult_Key, TMetadata]) model_core.Message[*model_analysis_pb.SuccessfulActionResult_Value, TReference]
}

type moduleOrRepositoryContext[TReference object.BasicReference, TMetadata BaseComputerReferenceMetadata] struct {
	computer               *baseComputer[TReference, TMetadata]
	context                context.Context
	environment            moduleOrRepositoryContextEnvironment[TReference, TMetadata]
	subdirectoryComponents []path.Component

	actionEncoder                      model_encoding.BinaryEncoder
	defaultWorkingDirectoryPath        *model_starlark.BarePath
	directoryCreationParameters        *model_filesystem.DirectoryCreationParameters
	directoryCreationParametersMessage *model_filesystem_pb.DirectoryCreationParameters
	directoryLoadOptions               *changeTrackingDirectoryLoadOptions[TReference]
	directoryReaders                   *DirectoryReaders[TReference]
	externalPath                       *model_starlark.BarePath
	fileCreationParameters             *model_filesystem.FileCreationParameters
	fileCreationParametersMessage      *model_filesystem_pb.FileCreationParameters
	fileReader                         *model_filesystem.FileReader[TReference]
	pathUnpackerInto                   unpack.UnpackerInto[*model_starlark.BarePath]
	repoPlatform                       model_core.Message[*model_analysis_pb.RegisteredRepoPlatform_Value, TReference]
	virtualRootScopeWalkerFactory      *path.VirtualRootScopeWalkerFactory

	inputRootDirectory *changeTrackingDirectory[TReference, TMetadata]
	patchedFiles       filesystem.FileReader
	patchedFilesWriter *model_filesystem.SectionWriter
}

func (c *baseComputer[TReference, TMetadata]) newModuleOrRepositoryContext(ctx context.Context, e moduleOrRepositoryContextEnvironment[TReference, TMetadata], subdirectoryComponents []path.Component) (*moduleOrRepositoryContext[TReference, TMetadata], error) {
	return &moduleOrRepositoryContext[TReference, TMetadata]{
		computer:               c,
		context:                ctx,
		environment:            e,
		subdirectoryComponents: subdirectoryComponents,

		inputRootDirectory: &changeTrackingDirectory[TReference, TMetadata]{},
	}, nil
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) release() {
	if mrc.patchedFiles != nil {
		mrc.patchedFiles.Close()
	}
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) resolveRepoDirectory() (*changeTrackingDirectory[TReference, TMetadata], error) {
	repoDirectory := mrc.inputRootDirectory
	for _, component := range mrc.subdirectoryComponents {
		childDirectory, ok := repoDirectory.directories[component]
		if !ok {
			return nil, errors.New("repository rule removed its own repository directory")
		}
		repoDirectory = childDirectory
	}
	return repoDirectory, nil
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) maybeGetActionEncoder() {
	if mrc.actionEncoder == nil {
		if v, ok := mrc.environment.GetActionEncoderObjectValue(&model_analysis_pb.ActionEncoderObject_Key{}); ok {
			mrc.actionEncoder = v
		}
	}
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) maybeGetDirectoryCreationParameters() {
	mrc.maybeGetDirectoryReaders()
	if mrc.directoryCreationParameters == nil {
		if v, ok := mrc.environment.GetDirectoryCreationParametersObjectValue(&model_analysis_pb.DirectoryCreationParametersObject_Key{}); ok {
			mrc.directoryCreationParameters = v
		}
	}
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) maybeGetDirectoryCreationParametersMessage() {
	if mrc.directoryCreationParametersMessage == nil {
		if v := mrc.environment.GetDirectoryCreationParametersValue(&model_analysis_pb.DirectoryCreationParameters_Key{}); v.IsSet() {
			mrc.directoryCreationParametersMessage = v.Message.DirectoryCreationParameters
		}
	}
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) maybeGetDirectoryReaders() {
	if mrc.directoryReaders == nil {
		if v, ok := mrc.environment.GetDirectoryReadersValue(&model_analysis_pb.DirectoryReaders_Key{}); ok {
			mrc.directoryReaders = v
			mrc.directoryLoadOptions = &changeTrackingDirectoryLoadOptions[TReference]{
				context:                 mrc.context,
				directoryContentsReader: v.DirectoryContents,
				leavesReader:            v.Leaves,
			}
		}
	}
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) maybeGetFileCreationParameters() {
	if mrc.fileCreationParameters == nil {
		if v, ok := mrc.environment.GetFileCreationParametersObjectValue(&model_analysis_pb.FileCreationParametersObject_Key{}); ok {
			mrc.fileCreationParameters = v
		}
	}
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) maybeGetFileCreationParametersMessage() {
	if mrc.fileCreationParametersMessage == nil {
		if v := mrc.environment.GetFileCreationParametersValue(&model_analysis_pb.FileCreationParameters_Key{}); v.IsSet() {
			mrc.fileCreationParametersMessage = v.Message.FileCreationParameters
		}
	}
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) maybeGetFileReader() {
	if mrc.fileReader == nil {
		if v, ok := mrc.environment.GetFileReaderValue(&model_analysis_pb.FileReader_Key{}); ok {
			mrc.fileReader = v
		}
	}
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) maybeInitializePatchedFiles() error {
	if mrc.patchedFiles == nil {
		patchedFiles, err := mrc.computer.filePool.NewFile(pool.ZeroHoleSource, 0)
		if err != nil {
			return err
		}
		mrc.patchedFiles = patchedFiles
		mrc.patchedFilesWriter = model_filesystem.NewSectionWriter(patchedFiles)
	}
	return nil
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) maybeGetRepoPlatform() {
	if !mrc.repoPlatform.IsSet() {
		mrc.repoPlatform = mrc.environment.GetRegisteredRepoPlatformValue(&model_analysis_pb.RegisteredRepoPlatform_Key{})
	}
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) maybeGetStableInputRootPath() error {
	if mrc.virtualRootScopeWalkerFactory == nil {
		stableInputRootPath, ok := mrc.environment.GetStableInputRootPathObjectValue(&model_analysis_pb.StableInputRootPathObject_Key{})
		if !ok {
			return evaluation.ErrMissingDependency
		}

		defaultWorkingDirectoryPath := stableInputRootPath
		for _, component := range mrc.subdirectoryComponents {
			defaultWorkingDirectoryPath = defaultWorkingDirectoryPath.Append(component)
		}

		virtualRootScopeWalkerFactory, err := path.NewVirtualRootScopeWalkerFactory(stableInputRootPath, nil)
		if err != nil {
			return err
		}

		mrc.defaultWorkingDirectoryPath = defaultWorkingDirectoryPath

		externalPath := stableInputRootPath.Append(model_starlark.ComponentExternal)
		mrc.externalPath = externalPath

		mrc.pathUnpackerInto = &externalRepoAddingPathUnpackerInto[TReference, TMetadata]{
			context: mrc,
			base: model_starlark.NewPathOrLabelOrStringUnpackerInto[TReference, TMetadata](
				func(canonicalRepo label.CanonicalRepo) (*model_starlark.BarePath, error) {
					// Map labels to paths under external/${repo}.
					return externalPath.Append(path.MustNewComponent(canonicalRepo.String())), nil
				},
				defaultWorkingDirectoryPath,
			),
		}
		mrc.virtualRootScopeWalkerFactory = virtualRootScopeWalkerFactory
	}
	return nil
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) maybeAddExternalRepo(repoName path.Component) error {
	if !slices.Equal(mrc.subdirectoryComponents, []path.Component{model_starlark.ComponentExternal, repoName}) {
		// Path belongs to an external repo that is different
		// from the repo that is currently being constructed.
		externalDirectory, err := mrc.inputRootDirectory.getOrCreateDirectory(model_starlark.ComponentExternal)
		if err != nil {
			return fmt.Errorf("Failed to create directory %#v: %w", model_starlark.ComponentStrExternal, err)
		}
		if err := externalDirectory.maybeLoadContents(mrc.directoryLoadOptions); err != nil {
			return fmt.Errorf("failed to load contents of %#v directory: %w", err)
		}

		if _, ok := externalDirectory.directories[repoName]; !ok {
			// External repo does not exist within
			// the input root. Fetch it.
			repo := mrc.environment.GetRepoValue(&model_analysis_pb.Repo_Key{
				CanonicalRepo: repoName.String(),
			})
			if !repo.IsSet() {
				return evaluation.ErrMissingDependency
			}
			repoDirectory, err := externalDirectory.getOrCreateDirectory(repoName)
			if err != nil {
				return fmt.Errorf("failed to create directory for repo: %w", err)
			}
			rootDirectoryReference := repo.Message.RootDirectoryReference
			if rootDirectoryReference == nil {
				return errors.New("root directory reference is not set")
			}
			repoDirectory.unmodifiedDirectory = model_core.Nested(
				repo,
				&model_filesystem_pb.Directory{
					Contents: &model_filesystem_pb.Directory_ContentsExternal{
						ContentsExternal: rootDirectoryReference,
					},
				},
			)
		}
	}
	return nil
}

func createDownloadSuccessResult[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](integrity *model_fetch_pb.SubresourceIntegrity, sha256 []byte) starlark.Value {
	fields := map[string]any{
		"success": starlark.Bool(true),
	}

	if integrity == nil {
		integrity = &model_fetch_pb.SubresourceIntegrity{
			HashAlgorithm: model_fetch_pb.SubresourceIntegrity_SHA256,
			Hash:          sha256,
		}
	}
	fields["integrity"] = starlark.String(strings.ToLower(model_fetch_pb.SubresourceIntegrity_HashAlgorithm_name[int32(integrity.HashAlgorithm)]) + "-" + base64.StdEncoding.EncodeToString(integrity.Hash))
	if integrity.HashAlgorithm == model_fetch_pb.SubresourceIntegrity_SHA256 {
		fields["sha256"] = starlark.String(hex.EncodeToString(integrity.Hash))
	}

	return model_starlark.NewStructFromDict[TReference, TMetadata](nil, fields)
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) doDownload(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	mrc.maybeGetDirectoryReaders()
	if err := mrc.maybeGetStableInputRootPath(); err != nil {
		return nil, err
	}
	if mrc.directoryLoadOptions == nil {
		return nil, evaluation.ErrMissingDependency
	}

	if len(args) > 8 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 8", b.Name(), len(args))
	}
	var urls []string
	output := mrc.defaultWorkingDirectoryPath
	sha256 := ""
	executable := false
	allowFail := false
	canonicalID := ""
	var auth map[string]map[string]string
	var headers map[string]string
	integrity := ""
	block := true
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"url", unpack.Bind(thread, &urls, unpack.Or([]unpack.UnpackerInto[[]string]{
			unpack.List(unpack.Stringer(unpack.URL)),
			unpack.Singleton(unpack.Stringer(unpack.URL)),
		})),
		"output?", unpack.Bind(thread, &output, mrc.pathUnpackerInto),
		"sha256?", unpack.Bind(thread, &sha256, unpack.String),
		"executable?", unpack.Bind(thread, &executable, unpack.Bool),
		"allow_fail?", unpack.Bind(thread, &allowFail, unpack.Bool),
		"canonical_id?", unpack.Bind(thread, &canonicalID, unpack.String),
		"auth?", unpack.Bind(thread, &auth, unpack.Dict(unpack.Stringer(unpack.URL), unpack.Dict(unpack.String, unpack.String))),
		"headers?", unpack.Bind(thread, &headers, unpack.Dict(unpack.String, unpack.String)),
		"integrity?", unpack.Bind(thread, &integrity, unpack.String),
		"block?", unpack.Bind(thread, &block, unpack.Bool),
	); err != nil {
		return nil, err
	}

	integrityMessage, err := parseSubresourceIntegrityOrSHA256(integrity, sha256)
	if err != nil {
		return nil, err
	}

	headersEntries := make([]*model_fetch_pb.Target_Header, 0, len(headers))
	for _, name := range slices.Sorted(maps.Keys(headers)) {
		headersEntries = append(headersEntries, &model_fetch_pb.Target_Header{
			Name:  name,
			Value: headers[name],
		})
	}

	fileContentsValue := mrc.environment.GetHttpFileContentsValue(&model_analysis_pb.HttpFileContents_Key{
		FetchOptions: &model_analysis_pb.HttpFetchOptions{
			Target: &model_fetch_pb.Target{
				Urls:      urls,
				Integrity: integrityMessage,
				Headers:   headersEntries,
				// TODO: Set auth!
			},
			AllowFail: allowFail,
		},
	})

	// Depending on whether "block" is set to true or not,
	// immediately attempt to insert the downloaded file into the
	// file system or delay it until result.wait() is called.
	if block {
		return mrc.completeDownload(output, executable, integrityMessage, fileContentsValue)
	}
	return model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
		"wait": starlark.NewBuiltin("repository_ctx.download.wait", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			return mrc.completeDownload(output, executable, integrityMessage, fileContentsValue)
		}),
	}), nil
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) completeDownload(output *model_starlark.BarePath, executable bool, integrity *model_fetch_pb.SubresourceIntegrity, fileContentsValue model_core.Message[*model_analysis_pb.HttpFileContents_Value, TReference]) (starlark.Value, error) {
	if !fileContentsValue.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	exists := fileContentsValue.Message.Exists
	if exists == nil {
		// File does not exist, or allow_fail was set and an
		// error occurred.
		return model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
			"success": starlark.Bool(false),
		}), nil
	}

	// Insert the downloaded file into the file system.
	r := &changeTrackingDirectoryNewFileResolver[TReference, TMetadata]{
		loadOptions: mrc.directoryLoadOptions,
		stack:       util.NewNonEmptyStack(mrc.inputRootDirectory),
	}
	if err := path.Resolve(output, mrc.virtualRootScopeWalkerFactory.New(r)); err != nil {
		return nil, fmt.Errorf("cannot resolve %#v: %w", output.GetUNIXString(), err)
	}
	if r.TerminalName == nil {
		return nil, fmt.Errorf("%#v does not resolve to a file", output.GetUNIXString())
	}

	if err := r.stack.Peek().setFile(
		mrc.directoryLoadOptions,
		*r.TerminalName,
		&changeTrackingFile[TReference, TMetadata]{
			isExecutable: executable,
			contents: unmodifiedFileContents[TReference, TMetadata]{
				contents: model_core.Nested(fileContentsValue, exists.Contents),
			},
		},
	); err != nil {
		return nil, err
	}

	return createDownloadSuccessResult[TReference, TMetadata](integrity, exists.Sha256), nil
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) doDownloadAndExtract(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	mrc.maybeGetDirectoryReaders()
	if err := mrc.maybeGetStableInputRootPath(); err != nil {
		return nil, err
	}
	if mrc.directoryLoadOptions == nil {
		return nil, evaluation.ErrMissingDependency
	}

	if len(args) > 9 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 9", b.Name(), len(args))
	}
	var urls []string
	output := mrc.defaultWorkingDirectoryPath
	sha256 := ""
	typeStr := ""
	var stripPrefix path.Parser = &path.EmptyBuilder
	allowFail := false
	canonicalID := ""
	var auth map[string]map[string]string
	var headers map[string]string
	integrity := ""
	var renameFiles map[string]string
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"url", unpack.Bind(thread, &urls, unpack.Or([]unpack.UnpackerInto[[]string]{
			unpack.List(unpack.Stringer(unpack.URL)),
			unpack.Singleton(unpack.Stringer(unpack.URL)),
		})),
		"output?", unpack.Bind(thread, &output, mrc.pathUnpackerInto),
		"sha256?", unpack.Bind(thread, &sha256, unpack.String),
		"type?", unpack.Bind(thread, &typeStr, unpack.String),
		"strip_prefix?", unpack.Bind(thread, &stripPrefix, unpack.PathParser(path.UNIXFormat)),
		"allow_fail?", unpack.Bind(thread, &allowFail, unpack.Bool),
		"canonical_id?", unpack.Bind(thread, &canonicalID, unpack.String),
		"auth?", unpack.Bind(thread, &auth, unpack.Dict(unpack.Stringer(unpack.URL), unpack.Dict(unpack.String, unpack.String))),
		"headers?", unpack.Bind(thread, &headers, unpack.Dict(unpack.String, unpack.String)),
		"integrity?", unpack.Bind(thread, &integrity, unpack.String),
		"rename_files?", unpack.Bind(thread, &renameFiles, unpack.Dict(unpack.String, unpack.String)),
		// For compatibility with Bazel < 8.
		"stripPrefix?", unpack.Bind(thread, &stripPrefix, unpack.PathParser(path.UNIXFormat)),
	); err != nil {
		return nil, err
	}

	integrityMessage, err := parseSubresourceIntegrityOrSHA256(integrity, sha256)
	if err != nil {
		return nil, err
	}

	var typeToMatch string
	if typeStr != "" {
		typeToMatch = "." + typeStr
	} else if len(urls) > 0 {
		typeToMatch = urls[0]
	} else {
		return nil, errors.New("no URLs provided")
	}
	archiveFormat, ok := inferArchiveFormatFromURL(typeToMatch)
	if !ok {
		return nil, fmt.Errorf("cannot derive archive format from file extension of %#v", typeToMatch)
	}

	archiveContentsValue := mrc.environment.GetHttpArchiveContentsValue(&model_analysis_pb.HttpArchiveContents_Key{
		FetchOptions: &model_analysis_pb.HttpFetchOptions{
			Target: &model_fetch_pb.Target{
				Integrity: integrityMessage,
				Urls:      urls,
			},
			AllowFail: allowFail,
		},
		Format: archiveFormat,
	})
	if !archiveContentsValue.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	if archiveContentsValue.Message.Exists == nil {
		// File does not exist, or allow_fail was set and an
		// error occurred.
		return model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
			"success": starlark.Bool(false),
		}), nil
	}

	// Determine which directory to place inside the file system.
	archiveRootDirectory := changeTrackingDirectory[TReference, TMetadata]{
		unmodifiedDirectory: model_core.Nested(
			archiveContentsValue,
			&model_filesystem_pb.Directory{
				Contents: &model_filesystem_pb.Directory_ContentsExternal{
					ContentsExternal: archiveContentsValue.Message.Exists.Contents,
				},
			},
		),
	}
	rootDirectoryResolver := changeTrackingDirectoryResolver[TReference, TMetadata]{
		loadOptions: mrc.directoryLoadOptions,
		stack:       util.NewNonEmptyStack(&archiveRootDirectory),
	}
	if err := path.Resolve(stripPrefix, path.NewRelativeScopeWalker(&rootDirectoryResolver)); err != nil {
		return nil, errors.New("failed to strip prefix from contents")
	}

	// Insert the directory into the file system.
	r := &changeTrackingDirectoryNewDirectoryResolver[TReference, TMetadata]{
		loadOptions: mrc.directoryLoadOptions,
		stack:       util.NewNonEmptyStack(mrc.inputRootDirectory),
	}
	if err := path.Resolve(output, mrc.virtualRootScopeWalkerFactory.New(r)); err != nil {
		return nil, fmt.Errorf("cannot resolve %#v: %w", output.GetUNIXString(), err)
	}

	*r.stack.Peek() = *rootDirectoryResolver.stack.Peek()

	return createDownloadSuccessResult[TReference, TMetadata](integrityMessage, archiveContentsValue.Message.Exists.Sha256), nil
}

// bytesToValidString converts a byte slice containing UTF-8 encoded
// characters to a string. If invalid UTF-8 sequences are encountered,
// U+FFFD is emitted.
func bytesToValidString(p []byte) (string, bool) {
	if utf8.Valid(p) {
		// Fast path: byte slice is already valid UTF-8.
		return string(p), true
	}

	// Slow path: byte slice contains one or more invalid sequences.
	var sb strings.Builder
	for {
		r, size := utf8.DecodeRune(p)
		if size == 0 {
			return sb.String(), false
		}
		sb.WriteRune(r)
		p = p[size:]
	}
}

func newArgumentsBuilder[TMetadata model_core.ReferenceMetadata](ctx context.Context, actionEncoder model_encoding.BinaryEncoder, referenceFormat object.ReferenceFormat, objectCapturer model_core.CreatedObjectCapturer[TMetadata]) (btree.Builder[*model_command_pb.ArgumentList_Element, TMetadata], btree.ParentNodeComputer[*model_command_pb.ArgumentList_Element, TMetadata]) {
	parentNodeComputer := btree.Capturing(ctx, objectCapturer, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_command_pb.ArgumentList_Element, object.LocalReference]) model_core.PatchedMessage[*model_command_pb.ArgumentList_Element, TMetadata] {
		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_command_pb.ArgumentList_Element {
			return &model_command_pb.ArgumentList_Element{
				Level: &model_command_pb.ArgumentList_Element_Parent{
					Parent: patcher.AddDecodableReference(createdObject),
				},
			}
		})
	})
	return btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			/* minimumSizeBytes = */ 1<<16,
			/* maximumSizeBytes = */ 1<<18,
			/* isParent = */ func(element *model_command_pb.ArgumentList_Element) bool {
				return element.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			actionEncoder,
			referenceFormat,
			parentNodeComputer,
		),
	), parentNodeComputer
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) doExecute(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	mrc.maybeGetActionEncoder()
	mrc.maybeGetDirectoryCreationParameters()
	mrc.maybeGetDirectoryCreationParametersMessage()
	mrc.maybeGetDirectoryReaders()
	mrc.maybeGetFileCreationParameters()
	mrc.maybeGetFileCreationParametersMessage()
	mrc.maybeGetFileReader()
	mrc.maybeGetRepoPlatform()
	stableInputRootPathError := mrc.maybeGetStableInputRootPath()
	if mrc.actionEncoder == nil ||
		mrc.directoryCreationParameters == nil ||
		mrc.directoryCreationParametersMessage == nil ||
		mrc.directoryReaders == nil ||
		mrc.fileCreationParameters == nil ||
		mrc.fileCreationParametersMessage == nil ||
		mrc.fileReader == nil ||
		!mrc.repoPlatform.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	if stableInputRootPathError != nil {
		return nil, stableInputRootPathError
	}

	var arguments []any
	timeout := int64(600)
	environment := map[string]string{}
	quiet := true
	workingDirectory := mrc.defaultWorkingDirectoryPath
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"arguments", unpack.Bind(thread, &arguments, unpack.List(
			unpack.Or([]unpack.UnpackerInto[any]{
				unpack.Decay(unpack.String),
				unpack.Decay(mrc.pathUnpackerInto),
			}),
		)),
		"timeout?", unpack.Bind(thread, &timeout, unpack.Int[int64]()),
		"environment?", unpack.Bind(thread, &environment, unpack.Dict(unpack.String, unpack.String)),
		"quiet?", unpack.Bind(thread, &quiet, unpack.Bool),
		"working_directory?", unpack.Bind(thread, &workingDirectory, mrc.pathUnpackerInto),
	); err != nil {
		return nil, err
	}

	// Inherit environment variables from
	// the repo platform.
	for _, environmentVariable := range mrc.repoPlatform.Message.RepositoryOsEnviron {
		if _, ok := environment[environmentVariable.Name]; !ok {
			environment[environmentVariable.Name] = environmentVariable.Value
		}
	}

	// Convert arguments and environment
	// variables to B-trees, so that they can
	// be attached to the Command message.
	referenceFormat := mrc.computer.referenceFormat
	argumentsBuilder, argumentsParentNodeComputer := newArgumentsBuilder(mrc.context, mrc.actionEncoder, referenceFormat, mrc.environment)
	for _, argument := range arguments {
		var argumentStr string
		switch typedArgument := argument.(type) {
		case string:
			argumentStr = typedArgument
		case *model_starlark.BarePath:
			argumentStr = typedArgument.GetUNIXString()
		default:
			panic("unexpected argument type")
		}
		if err := argumentsBuilder.PushChild(
			model_core.NewSimplePatchedMessage[TMetadata](&model_command_pb.ArgumentList_Element{
				Level: &model_command_pb.ArgumentList_Element_Leaf{
					Leaf: argumentStr,
				},
			}),
		); err != nil {
			return nil, err
		}
	}
	argumentList, err := argumentsBuilder.FinalizeList()
	if err != nil {
		return nil, err
	}

	environmentVariableList, envParentNodeComputer, err := convertDictToEnvironmentVariableList(
		mrc.context,
		environment,
		mrc.actionEncoder,
		referenceFormat,
		mrc.environment,
	)
	if err != nil {
		return nil, err
	}

	// The working directory should be implicitly created.
	if err := path.Resolve(
		workingDirectory,
		mrc.virtualRootScopeWalkerFactory.New(
			&changeTrackingDirectoryNewDirectoryResolver[TReference, TMetadata]{
				loadOptions: mrc.directoryLoadOptions,
				stack:       util.NewNonEmptyStack(mrc.inputRootDirectory),
			},
		),
	); err != nil {
		return nil, fmt.Errorf("failed to create working directory: %w", err)
	}

	// The command to execute is permitted to make changes to the
	// contents of the repo. These changes should be carried over to
	// any subsequent command and also become part of the repo's
	// final contents. Construct a pattern for capturing the
	// directory in the input root belonging to the repo.
	outputPathPatternChildren := model_core.NewSimplePatchedMessage[TMetadata]((*model_command_pb.PathPattern_Children)(nil))
	inlinedTreeOptions := mrc.computer.getInlinedTreeOptions()
	for i := len(mrc.subdirectoryComponents); i > 0; i-- {
		outputPathPatternChildren, err = model_command.PrependDirectoryToPathPatternChildren(
			mrc.context,
			mrc.subdirectoryComponents[i-1].String(),
			outputPathPatternChildren,
			mrc.actionEncoder,
			inlinedTreeOptions,
			mrc.environment,
		)
		if err != nil {
			return nil, err
		}
	}

	command, err := inlinedtree.Build(
		inlinedtree.CandidateList[*model_command_pb.Command, TMetadata]{
			// Fields that should always be inlined into the
			// Command message.
			inlinedtree.AlwaysInline(
				model_core.NewReferenceMessagePatcher[TMetadata](),
				func(command model_core.PatchedMessage[*model_command_pb.Command, TMetadata]) {
					command.Message.DirectoryCreationParameters = mrc.directoryCreationParametersMessage
					command.Message.FileCreationParameters = mrc.fileCreationParametersMessage
					command.Message.WorkingDirectory = workingDirectory.GetUNIXString()
					command.Message.StableInputRootPathUuid = repoRuleStableInputRootPathUUID
					command.Message.NeedsWritableInputFiles = true
				},
			),
			// Fields that can be stored externally if needed.
			{
				ExternalMessage: model_core.ProtoListToBinaryMarshaler(argumentList),
				Encoder:         mrc.actionEncoder,
				ParentAppender: func(
					command model_core.PatchedMessage[*model_command_pb.Command, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					arguments, err := btree.MaybeMergeNodes(
						argumentList.Message,
						externalObject,
						command.Patcher,
						argumentsParentNodeComputer,
					)
					if err != nil {
						return err
					}
					command.Message.Arguments = arguments
					return nil
				},
			},
			{
				ExternalMessage: model_core.ProtoListToBinaryMarshaler(environmentVariableList),
				Encoder:         mrc.actionEncoder,
				ParentAppender: func(
					command model_core.PatchedMessage[*model_command_pb.Command, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					environmentVariables, err := btree.MaybeMergeNodes(
						environmentVariableList.Message,
						externalObject,
						command.Patcher,
						envParentNodeComputer,
					)
					if err != nil {
						return err
					}
					command.Message.EnvironmentVariables = environmentVariables
					return nil
				},
			},
			{
				ExternalMessage: model_core.ProtoToBinaryMarshaler(outputPathPatternChildren),
				Encoder:         mrc.actionEncoder,
				ParentAppender: inlinedtree.Capturing(mrc.context, mrc.environment, func(
					command model_core.PatchedMessage[*model_command_pb.Command, TMetadata],
					externalObject *model_core.Decodable[model_core.MetadataEntry[TMetadata]],
				) {
					command.Message.OutputPathPattern = model_command.GetPathPatternWithChildren(
						outputPathPatternChildren,
						externalObject,
						command.Patcher,
					)
				}),
			},
		},
		inlinedTreeOptions,
	)
	if err != nil {
		return nil, err
	}
	createdCommand, err := model_core.MarshalAndEncode(
		model_core.ProtoToBinaryMarshaler(command),
		referenceFormat,
		mrc.actionEncoder,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create command: %w", err)
	}

	inputRootReference, err := mrc.computer.createMerkleTreeFromChangeTrackingDirectory(mrc.context, mrc.environment, mrc.inputRootDirectory, mrc.directoryCreationParameters, mrc.directoryReaders, mrc.fileCreationParameters, mrc.patchedFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to create Merkle tree of root directory: %w", err)
	}

	action, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (encoding.BinaryMarshaler, error) {
		patcher.Merge(inputRootReference.Patcher)
		commandReference, err := patcher.CaptureAndAddDecodableReference(mrc.context, createdCommand, mrc.environment)
		if err != nil {
			return nil, err
		}
		return model_core.NewProtoBinaryMarshaler(&model_command_pb.Action{
			CommandReference:   commandReference,
			InputRootReference: inputRootReference.Message,
		}), nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create action: %w", err)
	}
	createdAction, err := model_core.MarshalAndEncode(action, referenceFormat, mrc.actionEncoder)
	if err != nil {
		return nil, fmt.Errorf("failed to encode action: %w", err)
	}

	// Execute the command.
	actionResultKey, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.ActionResult_Key, error) {
		actionReference, err := patcher.CaptureAndAddDecodableReference(mrc.context, createdAction, mrc.environment)
		if err != nil {
			return nil, err
		}
		return &model_analysis_pb.ActionResult_Key{
			ExecuteRequest: &model_analysis_pb.ExecuteRequest{
				PlatformPkixPublicKey: mrc.repoPlatform.Message.ExecPkixPublicKey,
				ActionReference:       actionReference,
				ExecutionTimeout:      &durationpb.Duration{Seconds: timeout},
			},
		}, nil
	})
	actionResult := mrc.environment.GetActionResultValue(actionResultKey)
	if !actionResult.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}

	// Extract standard output and standard error from the results.
	outputs, err := model_parser.MaybeDereference(mrc.context, mrc.directoryReaders.CommandOutputs, model_core.Nested(actionResult, actionResult.Message.OutputsReference))
	if err != nil {
		return nil, fmt.Errorf("failed to obtain outputs from action result: %w", err)
	}

	stdoutEntry, err := model_filesystem.NewFileContentsEntryFromProto(
		model_core.Nested(outputs, outputs.Message.GetStdout()),
	)
	if err != nil {
		return nil, fmt.Errorf("invalid standard output entry: %w", err)
	}
	stdout, err := mrc.fileReader.FileReadAll(mrc.context, stdoutEntry, 1<<22)
	if err != nil {
		return nil, fmt.Errorf("failed to read standard output: %w", err)
	}

	stderrEntry, err := model_filesystem.NewFileContentsEntryFromProto(
		model_core.Nested(outputs, outputs.Message.GetStderr()),
	)
	if err != nil {
		return nil, fmt.Errorf("invalid standard error entry: %w", err)
	}
	stderr, err := mrc.fileReader.FileReadAll(mrc.context, stderrEntry, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("failed to read standard error: %w", err)
	}

	// The command may have mutated the repo's contents. Extract the
	// repo directory contents from the results and copy it into the
	// input root.
	outputRootDirectory := changeTrackingDirectory[TReference, TMetadata]{
		unmodifiedDirectory: model_core.Nested(outputs, &model_filesystem_pb.Directory{
			Contents: &model_filesystem_pb.Directory_ContentsInline{
				ContentsInline: outputs.Message.GetOutputRoot(),
			},
		}),
	}
	inputRepoDirectory := mrc.inputRootDirectory
	outputRepoDirectory := &outputRootDirectory
	for _, component := range mrc.subdirectoryComponents {
		if err := inputRepoDirectory.maybeLoadContents(mrc.directoryLoadOptions); err != nil {
			return nil, err
		}
		if err := outputRepoDirectory.maybeLoadContents(mrc.directoryLoadOptions); err != nil {
			return nil, err
		}
		var ok bool
		inputRepoDirectory, ok = inputRepoDirectory.directories[component]
		if !ok {
			return nil, fmt.Errorf("repo directory no longer exists")
		}
		outputRepoDirectory, ok = outputRepoDirectory.directories[component]
		if !ok {
			return nil, fmt.Errorf("repo directory no longer exists")
		}
	}
	*inputRepoDirectory = *outputRepoDirectory

	stderrStr, _ := bytesToValidString(stderr)
	stdoutStr, _ := bytesToValidString(stdout)
	return model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
		"return_code": starlark.MakeInt64(actionResult.Message.ExitCode),
		"stderr":      starlark.String(stderrStr),
		"stdout":      starlark.String(stdoutStr),
	}), nil
}

func (moduleOrRepositoryContext[TReference, TMetadata]) doExtract(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return nil, errors.New("TODO: Implement!")
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) doFile(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	mrc.maybeGetDirectoryReaders()
	stableInputRootPathError := mrc.maybeGetStableInputRootPath()
	if mrc.directoryLoadOptions == nil {
		return nil, evaluation.ErrMissingDependency
	}
	if stableInputRootPathError != nil {
		return nil, stableInputRootPathError
	}

	var filePath *model_starlark.BarePath
	content := ""
	executable := true
	legacyUTF8 := true
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"path", unpack.Bind(thread, &filePath, mrc.pathUnpackerInto),
		"content?", unpack.Bind(thread, &content, unpack.String),
		"executable?", unpack.Bind(thread, &executable, unpack.Bool),
		"legacy_utf8?", unpack.Bind(thread, &legacyUTF8, unpack.Bool),
	); err != nil {
		return nil, err
	}

	r := &changeTrackingDirectoryNewFileResolver[TReference, TMetadata]{
		loadOptions: mrc.directoryLoadOptions,
		stack:       util.NewNonEmptyStack(mrc.inputRootDirectory),
	}
	if err := path.Resolve(filePath, mrc.virtualRootScopeWalkerFactory.New(r)); err != nil {
		return nil, fmt.Errorf("cannot resolve %#v: %w", filePath.GetUNIXString(), err)
	}
	if r.TerminalName == nil {
		return nil, fmt.Errorf("%#v does not resolve to a file", filePath.GetUNIXString())
	}

	if err := mrc.maybeInitializePatchedFiles(); err != nil {
		return nil, err
	}

	// TODO: Do UTF-8 -> ISO-8859-1
	// conversion if legacy_utf8=False.
	patchedFileOffsetBytes := mrc.patchedFilesWriter.GetOffsetBytes()
	if _, err := mrc.patchedFilesWriter.WriteString(content); err != nil {
		return nil, fmt.Errorf("failed to write to file at %#v: %w", filePath.GetUNIXString(), err)
	}

	if err := r.stack.Peek().setFile(
		mrc.directoryLoadOptions,
		*r.TerminalName,
		&changeTrackingFile[TReference, TMetadata]{
			isExecutable: executable,
			contents: patchedFileContents[TReference, TMetadata]{
				offsetBytes: patchedFileOffsetBytes,
				sizeBytes:   mrc.patchedFilesWriter.GetOffsetBytes() - patchedFileOffsetBytes,
			},
		},
	); err != nil {
		return nil, err
	}

	return starlark.None, nil
}

func (moduleOrRepositoryContext[TReference, TMetadata]) doGetenv(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name string
	var defaultValue *string
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"name", unpack.Bind(thread, &name, unpack.String),
		"default?", unpack.Bind(thread, &defaultValue, unpack.IfNotNone(unpack.Pointer(unpack.String))),
	); err != nil {
		return nil, err
	}

	// TODO: Provide a real implementation!
	if defaultValue == nil {
		return starlark.None, nil
	}
	return starlark.String(*defaultValue), nil
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) doPath(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := mrc.maybeGetStableInputRootPath(); err != nil {
		return nil, err
	}

	var filePath *model_starlark.BarePath
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"path", unpack.Bind(thread, &filePath, mrc.pathUnpackerInto),
	); err != nil {
		return nil, err
	}
	return model_starlark.NewPath(filePath, mrc), nil
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) doRead(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	mrc.maybeGetDirectoryReaders()
	mrc.maybeGetFileReader()
	if err := mrc.maybeGetStableInputRootPath(); err != nil {
		return nil, err
	}
	if mrc.directoryLoadOptions == nil || mrc.fileReader == nil {
		return nil, evaluation.ErrMissingDependency
	}

	if len(args) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args))
	}
	var filePath *model_starlark.BarePath
	var watch string
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"path", unpack.Bind(thread, &filePath, mrc.pathUnpackerInto),
		"watch?", unpack.Bind(thread, &watch, unpack.String),
	); err != nil {
		return nil, err
	}

	r := &changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]{
		loadOptions: mrc.directoryLoadOptions,
		stack:       util.NewNonEmptyStack(mrc.inputRootDirectory),
	}
	if err := path.Resolve(filePath, mrc.virtualRootScopeWalkerFactory.New(r)); err != nil {
		return nil, fmt.Errorf("cannot resolve %#v: %w", filePath.GetUNIXString(), err)
	}

	if r.gotScope {
		// Path resolves to a location inside the input root.
		// Read the file directly.
		patchedFile, err := r.getFile()
		if err != nil {
			return nil, fmt.Errorf("cannot get file %#v: %w", filePath.GetUNIXString(), err)
		}

		f, err := patchedFile.contents.openRead(mrc.context, mrc.fileReader, mrc.patchedFiles)
		if err != nil {
			return nil, fmt.Errorf("failed to open file %#v: %w", filePath.GetUNIXString(), err)
		}

		// TODO: Limit maximum read size!
		data, err := io.ReadAll(f)
		if err != nil {
			return nil, fmt.Errorf("failed to read file %#v: %w", filePath.GetUNIXString(), err)
		}
		return starlark.String(string(data)), nil
	}

	// Path resolves to a location that is not part of the input
	// root (e.g., a file provided by the operating system or stored
	// in the home directory). Invoke "cat" to read the file's
	// contents.
	mrc.maybeGetActionEncoder()
	mrc.maybeGetDirectoryCreationParametersMessage()
	mrc.maybeGetFileCreationParameters()
	mrc.maybeGetFileCreationParametersMessage()
	mrc.maybeGetRepoPlatform()
	stableInputRootPathError := mrc.maybeGetStableInputRootPath()
	if mrc.actionEncoder == nil ||
		mrc.directoryCreationParametersMessage == nil ||
		mrc.fileCreationParameters == nil ||
		mrc.fileCreationParametersMessage == nil ||
		mrc.fileReader == nil ||
		!mrc.repoPlatform.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	if stableInputRootPathError != nil {
		return nil, stableInputRootPathError
	}

	environment := map[string]string{}
	for _, environmentVariable := range mrc.repoPlatform.Message.RepositoryOsEnviron {
		if _, ok := environment[environmentVariable.Name]; !ok {
			environment[environmentVariable.Name] = environmentVariable.Value
		}
	}
	referenceFormat := mrc.computer.referenceFormat
	environmentVariableList, _, err := convertDictToEnvironmentVariableList(
		mrc.context,
		environment,
		mrc.actionEncoder,
		referenceFormat,
		mrc.environment,
	)
	if err != nil {
		return nil, err
	}

	// TODO: This should use inlinedtree.Build().
	createdCommand, err := model_core.MarshalAndEncode(
		model_core.NewPatchedMessage(
			model_core.NewProtoBinaryMarshaler(&model_command_pb.Command{
				Arguments: []*model_command_pb.ArgumentList_Element{
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: "cat",
						},
					},
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: filePath.GetUNIXString(),
						},
					},
				},
				EnvironmentVariables:        environmentVariableList.Message,
				DirectoryCreationParameters: mrc.directoryCreationParametersMessage,
				FileCreationParameters:      mrc.fileCreationParametersMessage,
				WorkingDirectory:            "/",
				StableInputRootPathUuid:     repoRuleStableInputRootPathUUID,
			}),
			environmentVariableList.Patcher,
		),
		referenceFormat,
		mrc.actionEncoder,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create command: %w", err)
	}

	inputRootReference, err := mrc.computer.createMerkleTreeFromChangeTrackingDirectory(mrc.context, mrc.environment, mrc.inputRootDirectory, mrc.directoryCreationParameters, mrc.directoryReaders, mrc.fileCreationParameters, mrc.patchedFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to create Merkle tree of root directory: %w", err)
	}

	action, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (encoding.BinaryMarshaler, error) {
		patcher.Merge(inputRootReference.Patcher)
		commandReference, err := patcher.CaptureAndAddDecodableReference(mrc.context, createdCommand, mrc.environment)
		if err != nil {
			return nil, err
		}
		return model_core.NewProtoBinaryMarshaler(&model_command_pb.Action{
			CommandReference:   commandReference,
			InputRootReference: inputRootReference.Message,
		}), nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create action: %w", err)
	}
	createdAction, err := model_core.MarshalAndEncode(action, referenceFormat, mrc.actionEncoder)
	if err != nil {
		return nil, fmt.Errorf("failed to encode action: %w", err)
	}

	// Execute the command.
	actionResultKey, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.SuccessfulActionResult_Key, error) {
		actionReference, err := patcher.CaptureAndAddDecodableReference(mrc.context, createdAction, mrc.environment)
		if err != nil {
			return nil, err
		}
		return &model_analysis_pb.SuccessfulActionResult_Key{
			ExecuteRequest: &model_analysis_pb.ExecuteRequest{
				PlatformPkixPublicKey: mrc.repoPlatform.Message.ExecPkixPublicKey,
				ActionReference:       actionReference,
				ExecutionTimeout:      &durationpb.Duration{Seconds: 300},
			},
		}, nil
	})
	if err != nil {
		return nil, err
	}
	actionResult := mrc.environment.GetSuccessfulActionResultValue(actionResultKey)
	if !actionResult.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}

	// Extract standard output.
	outputs, err := model_parser.MaybeDereference(mrc.context, mrc.directoryReaders.CommandOutputs, model_core.Nested(actionResult, actionResult.Message.OutputsReference))
	if err != nil {
		return nil, fmt.Errorf("failed to obtain outputs from action result: %w", err)
	}

	stdoutEntry, err := model_filesystem.NewFileContentsEntryFromProto(
		model_core.Nested(outputs, outputs.Message.GetStdout()),
	)
	if err != nil {
		return nil, fmt.Errorf("invalid standard output entry: %w", err)
	}
	stdout, err := mrc.fileReader.FileReadAll(mrc.context, stdoutEntry, 1<<22)
	if err != nil {
		return nil, fmt.Errorf("failed to read standard output: %w", err)
	}
	return starlark.String(string(stdout)), nil
}

func (moduleOrRepositoryContext[TReference, TMetadata]) doReportProgress(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var status string
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"status", unpack.Bind(thread, &status, unpack.String),
	); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) doWatch(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := mrc.maybeGetStableInputRootPath(); err != nil {
		return nil, err
	}

	var filePath *model_starlark.BarePath
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"path", unpack.Bind(thread, &filePath, mrc.pathUnpackerInto),
	); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) doWhich(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	mrc.maybeGetActionEncoder()
	mrc.maybeGetDirectoryCreationParameters()
	mrc.maybeGetDirectoryCreationParametersMessage()
	mrc.maybeGetDirectoryReaders()
	mrc.maybeGetFileCreationParametersMessage()
	mrc.maybeGetFileReader()
	mrc.maybeGetRepoPlatform()
	if mrc.actionEncoder == nil ||
		mrc.directoryCreationParameters == nil ||
		mrc.directoryCreationParametersMessage == nil ||
		mrc.directoryReaders == nil ||
		mrc.fileCreationParametersMessage == nil ||
		mrc.fileReader == nil ||
		!mrc.repoPlatform.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}

	var program string
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"program", unpack.Bind(thread, &program, unpack.String),
	); err != nil {
		return nil, err
	}

	environment := map[string]string{}
	for _, environmentVariable := range mrc.repoPlatform.Message.RepositoryOsEnviron {
		environment[environmentVariable.Name] = environmentVariable.Value
	}
	referenceFormat := mrc.computer.referenceFormat
	environmentVariableList, _, err := convertDictToEnvironmentVariableList(
		mrc.context,
		environment,
		mrc.actionEncoder,
		referenceFormat,
		mrc.environment,
	)
	if err != nil {
		return nil, err
	}

	// TODO: This should use inlinedtree.Build().
	createdCommand, err := model_core.MarshalAndEncode(
		model_core.NewPatchedMessage(
			model_core.NewProtoBinaryMarshaler(&model_command_pb.Command{
				Arguments: []*model_command_pb.ArgumentList_Element{
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: "sh",
						},
					},
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: "-c",
						},
					},
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: shellquote.Join("command", "-v", "--", program),
						},
					},
				},
				EnvironmentVariables:        environmentVariableList.Message,
				DirectoryCreationParameters: mrc.directoryCreationParametersMessage,
				FileCreationParameters:      mrc.fileCreationParametersMessage,
				WorkingDirectory:            path.EmptyBuilder.GetUNIXString(),
			}),
			environmentVariableList.Patcher,
		),
		referenceFormat,
		mrc.actionEncoder,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create command: %w", err)
	}

	createdInputRoot, err := model_core.MarshalAndEncode(
		model_core.NewSimplePatchedMessage[TMetadata](
			model_core.NewProtoBinaryMarshaler(&model_filesystem_pb.DirectoryContents{
				Leaves: &model_filesystem_pb.DirectoryContents_LeavesInline{
					LeavesInline: &model_filesystem_pb.Leaves{},
				},
			}),
		),
		referenceFormat,
		mrc.directoryCreationParameters.GetEncoder(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create input root: %w", err)
	}

	action, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (encoding.BinaryMarshaler, error) {
		commandReference, err := patcher.CaptureAndAddDecodableReference(mrc.context, createdCommand, mrc.environment)
		if err != nil {
			return nil, err
		}
		inputRootReference, err := patcher.CaptureAndAddDecodableReference(mrc.context, createdInputRoot, mrc.environment)
		if err != nil {
			return nil, err
		}
		return model_core.NewProtoBinaryMarshaler(&model_command_pb.Action{
			CommandReference: commandReference,
			// TODO: We shouldn't be handcrafting a
			// DirectoryReference here.
			InputRootReference: &model_filesystem_pb.DirectoryReference{
				Reference:                      inputRootReference,
				MaximumSymlinkEscapementLevels: &wrapperspb.UInt32Value{},
			},
		}), nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create action: %w", err)
	}
	createdAction, err := model_core.MarshalAndEncode(action, referenceFormat, mrc.actionEncoder)
	if err != nil {
		return nil, fmt.Errorf("failed to encode action: %w", err)
	}

	// Invoke command.
	actionResultKey, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.ActionResult_Key, error) {
		actionReference, err := patcher.CaptureAndAddDecodableReference(mrc.context, createdAction, mrc.environment)
		if err != nil {
			return nil, err
		}
		return &model_analysis_pb.ActionResult_Key{
			ExecuteRequest: &model_analysis_pb.ExecuteRequest{
				PlatformPkixPublicKey: mrc.repoPlatform.Message.ExecPkixPublicKey,
				ActionReference:       actionReference,
				ExecutionTimeout:      &durationpb.Duration{Seconds: 60},
			},
		}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create action key: %w", err)
	}
	actionResult := mrc.environment.GetActionResultValue(actionResultKey)
	if !actionResult.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}

	if actionResult.Message.ExitCode == 0 {
		// Capture the standard output of "command -v" and trim the
		// trailing newline character that it adds.
		outputs, err := model_parser.MaybeDereference(mrc.context, mrc.directoryReaders.CommandOutputs, model_core.Nested(actionResult, actionResult.Message.OutputsReference))
		if err != nil {
			return nil, fmt.Errorf("failed to obtain outputs from action result: %w", err)
		}

		stdoutEntry, err := model_filesystem.NewFileContentsEntryFromProto(
			model_core.Nested(outputs, outputs.Message.GetStdout()),
		)
		if err != nil {
			return nil, fmt.Errorf("invalid standard output entry: %w", err)
		}
		stdout, err := mrc.fileReader.FileReadAll(mrc.context, stdoutEntry, 1<<20)
		if err != nil {
			return nil, fmt.Errorf("failed to read standard output: %w", err)
		}
		stdoutStr := strings.TrimSuffix(string(stdout), "\n")
		var resolver model_starlark.PathResolver
		if err := path.Resolve(
			path.UNIXFormat.NewParser(stdoutStr),
			path.NewAbsoluteScopeWalker(&resolver),
		); err != nil {
			return nil, fmt.Errorf("failed to resolve path %#v: %w", stdoutStr, err)
		}
		return model_starlark.NewPath(resolver.CurrentPath, mrc), nil
	} else {
		// A non-zero exit code indicates that the utility could
		// not be found.
		//
		// https://pubs.opengroup.org/onlinepubs/9799919799/utilities/command.html
		return starlark.None, nil
	}
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) Exists(p *model_starlark.BarePath) (bool, error) {
	mrc.maybeGetDirectoryReaders()
	if err := mrc.maybeGetStableInputRootPath(); err != nil {
		return false, err
	}
	if mrc.directoryLoadOptions == nil {
		return false, evaluation.ErrMissingDependency
	}

	r := &changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]{
		loadOptions: mrc.directoryLoadOptions,
		stack:       util.NewNonEmptyStack(mrc.inputRootDirectory),
	}
	if err := path.Resolve(p, mrc.virtualRootScopeWalkerFactory.New(r)); err != nil {
		if errors.Is(err, errDirectoryDoesNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("cannot resolve %#v: %w", p.GetUNIXString(), err)
	}
	if r.gotScope {
		if r.TerminalName == nil {
			// No trailing filename, meaning the path corresponds to
			// the directory that we're in right now.
			return true, nil
		}

		d := r.stack.Peek()
		if _, ok := d.directories[*r.TerminalName]; ok {
			return true, nil
		}
		if _, ok := d.files[*r.TerminalName]; ok {
			return true, nil
		}
		if _, ok := d.symlinks[*r.TerminalName]; ok {
			return true, nil
		}
		return false, nil
	}

	// Path resolves to a location that is not part of the input
	// root (e.g., a file provided by the operating system or stored
	// in the home directory). Invoke "test -e" to check the file's
	// existence.
	mrc.maybeGetActionEncoder()
	mrc.maybeGetDirectoryCreationParametersMessage()
	mrc.maybeGetFileCreationParameters()
	mrc.maybeGetFileCreationParametersMessage()
	mrc.maybeGetRepoPlatform()
	if mrc.actionEncoder == nil ||
		mrc.directoryCreationParametersMessage == nil ||
		mrc.fileCreationParameters == nil ||
		mrc.fileCreationParametersMessage == nil ||
		!mrc.repoPlatform.IsSet() {
		return false, evaluation.ErrMissingDependency
	}

	environment := map[string]string{}
	for _, environmentVariable := range mrc.repoPlatform.Message.RepositoryOsEnviron {
		if _, ok := environment[environmentVariable.Name]; !ok {
			environment[environmentVariable.Name] = environmentVariable.Value
		}
	}
	referenceFormat := mrc.computer.referenceFormat
	environmentVariableList, _, err := convertDictToEnvironmentVariableList(
		mrc.context,
		environment,
		mrc.actionEncoder,
		referenceFormat,
		mrc.environment,
	)
	if err != nil {
		return false, err
	}

	// TODO: This should use inlinedtree.Build().
	createdCommand, err := model_core.MarshalAndEncode(
		model_core.NewPatchedMessage(
			model_core.NewProtoBinaryMarshaler(&model_command_pb.Command{
				Arguments: []*model_command_pb.ArgumentList_Element{
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: "sh",
						},
					},
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: "-c",
						},
					},
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: shellquote.Join("test", "-e", p.GetUNIXString()),
						},
					},
				},
				EnvironmentVariables:        environmentVariableList.Message,
				DirectoryCreationParameters: mrc.directoryCreationParametersMessage,
				FileCreationParameters:      mrc.fileCreationParametersMessage,
				WorkingDirectory:            "/",
				StableInputRootPathUuid:     repoRuleStableInputRootPathUUID,
			}),
			environmentVariableList.Patcher,
		),
		referenceFormat,
		mrc.actionEncoder,
	)
	if err != nil {
		return false, fmt.Errorf("failed to create command: %w", err)
	}

	inputRootReference, err := mrc.computer.createMerkleTreeFromChangeTrackingDirectory(mrc.context, mrc.environment, mrc.inputRootDirectory, mrc.directoryCreationParameters, mrc.directoryReaders, mrc.fileCreationParameters, mrc.patchedFiles)
	if err != nil {
		return false, fmt.Errorf("failed to create Merkle tree of root directory: %w", err)
	}

	action, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (encoding.BinaryMarshaler, error) {
		patcher.Merge(inputRootReference.Patcher)
		commandReference, err := patcher.CaptureAndAddDecodableReference(mrc.context, createdCommand, mrc.environment)
		if err != nil {
			return nil, err
		}
		return model_core.NewProtoBinaryMarshaler(&model_command_pb.Action{
			CommandReference:   commandReference,
			InputRootReference: inputRootReference.Message,
		}), nil
	})
	if err != nil {
		return false, fmt.Errorf("failed to create action: %w", err)
	}
	createdAction, err := model_core.MarshalAndEncode(action, referenceFormat, mrc.actionEncoder)
	if err != nil {
		return false, fmt.Errorf("failed to encode action: %w", err)
	}

	// Execute the command.
	actionResultKey, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.ActionResult_Key, error) {
		actionReference, err := patcher.CaptureAndAddDecodableReference(mrc.context, createdAction, mrc.environment)
		if err != nil {
			return nil, err
		}
		return &model_analysis_pb.ActionResult_Key{
			ExecuteRequest: &model_analysis_pb.ExecuteRequest{
				PlatformPkixPublicKey: mrc.repoPlatform.Message.ExecPkixPublicKey,
				ActionReference:       actionReference,
				ExecutionTimeout:      &durationpb.Duration{Seconds: 300},
			},
		}, nil
	})
	if err != nil {
		return false, fmt.Errorf("failed to create action result key: %w", err)
	}
	actionResult := mrc.environment.GetActionResultValue(actionResultKey)
	if !actionResult.IsSet() {
		return false, evaluation.ErrMissingDependency
	}
	return actionResult.Message.ExitCode == 0, nil
}

func (moduleOrRepositoryContext[TReference, TMetadata]) IsDir(p *model_starlark.BarePath) (bool, error) {
	return false, fmt.Errorf("TODO: Implement path.is_dir(%#v)!", p.GetUNIXString())
}

func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) Readdir(p *model_starlark.BarePath) ([]path.Component, error) {
	mrc.maybeGetDirectoryReaders()
	if err := mrc.maybeGetStableInputRootPath(); err != nil {
		return nil, err
	}
	if mrc.directoryLoadOptions == nil {
		return nil, evaluation.ErrMissingDependency
	}

	r := &changeTrackingDirectoryResolver[TReference, TMetadata]{
		loadOptions: mrc.directoryLoadOptions,
		stack:       util.NewNonEmptyStack(mrc.inputRootDirectory),
	}
	if err := path.Resolve(p, mrc.virtualRootScopeWalkerFactory.New(r)); err != nil {
		if errors.Is(err, errDirectoryDoesNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot resolve %#v: %w", p.GetUNIXString(), err)
	}
	if r.gotScope {
		return nil, fmt.Errorf("TODO: Implement path.readdir(%#v) for paths in the input root!", p.GetUNIXString())
	}

	// Path resolves to a location that is not part of the input
	// root (e.g., a directory provided by the operating system or
	// stored in the home directory). Invoke "ls -A" to obtain a
	// directory listing.
	//
	// Ideally we'd run "find . -depth 1 -print0", but that does not
	// return paths in sorted order. Sorting on our end leads to
	// unnecessary cache invalidation.
	mrc.maybeGetActionEncoder()
	mrc.maybeGetDirectoryCreationParametersMessage()
	mrc.maybeGetDirectoryReaders()
	mrc.maybeGetFileCreationParameters()
	mrc.maybeGetFileCreationParametersMessage()
	mrc.maybeGetFileReader()
	mrc.maybeGetRepoPlatform()
	if mrc.actionEncoder == nil ||
		mrc.directoryCreationParametersMessage == nil ||
		mrc.directoryReaders == nil ||
		mrc.fileCreationParameters == nil ||
		mrc.fileCreationParametersMessage == nil ||
		mrc.fileReader == nil ||
		!mrc.repoPlatform.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}

	environment := map[string]string{}
	for _, environmentVariable := range mrc.repoPlatform.Message.RepositoryOsEnviron {
		if _, ok := environment[environmentVariable.Name]; !ok {
			environment[environmentVariable.Name] = environmentVariable.Value
		}
	}
	referenceFormat := mrc.computer.referenceFormat
	environmentVariableList, _, err := convertDictToEnvironmentVariableList(
		mrc.context,
		environment,
		mrc.actionEncoder,
		referenceFormat,
		mrc.environment,
	)
	if err != nil {
		return nil, err
	}

	// TODO: This should use inlinedtree.Build().
	createdCommand, err := model_core.MarshalAndEncode(
		model_core.NewPatchedMessage(
			model_core.NewProtoBinaryMarshaler(&model_command_pb.Command{
				Arguments: []*model_command_pb.ArgumentList_Element{
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: "ls",
						},
					},
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: "-A",
						},
					},
				},
				EnvironmentVariables:        environmentVariableList.Message,
				DirectoryCreationParameters: mrc.directoryCreationParametersMessage,
				FileCreationParameters:      mrc.fileCreationParametersMessage,
				WorkingDirectory:            p.GetUNIXString(),
				StableInputRootPathUuid:     repoRuleStableInputRootPathUUID,
			}),
			environmentVariableList.Patcher,
		),
		referenceFormat,
		mrc.actionEncoder,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create command: %w", err)
	}

	inputRootReference, err := mrc.computer.createMerkleTreeFromChangeTrackingDirectory(mrc.context, mrc.environment, mrc.inputRootDirectory, mrc.directoryCreationParameters, mrc.directoryReaders, mrc.fileCreationParameters, mrc.patchedFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to create Merkle tree of root directory: %w", err)
	}

	action, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (encoding.BinaryMarshaler, error) {
		patcher.Merge(inputRootReference.Patcher)
		commandReference, err := patcher.CaptureAndAddDecodableReference(mrc.context, createdCommand, mrc.environment)
		if err != nil {
			return nil, err
		}
		return model_core.NewProtoBinaryMarshaler(&model_command_pb.Action{
			CommandReference:   commandReference,
			InputRootReference: inputRootReference.Message,
		}), nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create action: %w", err)
	}
	createdAction, err := model_core.MarshalAndEncode(action, referenceFormat, mrc.actionEncoder)
	if err != nil {
		return nil, fmt.Errorf("failed to encode action: %w", err)
	}

	actionResultKey, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.SuccessfulActionResult_Key, error) {
		actionReference, err := patcher.CaptureAndAddDecodableReference(
			mrc.context,
			createdAction,
			mrc.environment,
		)
		if err != nil {
			return nil, err
		}
		return &model_analysis_pb.SuccessfulActionResult_Key{
			ExecuteRequest: &model_analysis_pb.ExecuteRequest{
				PlatformPkixPublicKey: mrc.repoPlatform.Message.ExecPkixPublicKey,
				ActionReference:       actionReference,
				ExecutionTimeout:      &durationpb.Duration{Seconds: 300},
			},
		}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create action result key: %w", err)
	}
	actionResult := mrc.environment.GetSuccessfulActionResultValue(actionResultKey)
	if !actionResult.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}

	// Extract filenames from the output of "ls".
	outputs, err := model_parser.MaybeDereference(mrc.context, mrc.directoryReaders.CommandOutputs, model_core.Nested(actionResult, actionResult.Message.OutputsReference))
	if err != nil {
		return nil, fmt.Errorf("failed to obtain outputs from action result: %w", err)
	}
	stdoutEntry, err := model_filesystem.NewFileContentsEntryFromProto(
		model_core.Nested(outputs, outputs.Message.GetStdout()),
	)
	if err != nil {
		return nil, fmt.Errorf("invalid standard output entry: %w", err)
	}
	stdout, err := mrc.fileReader.FileReadAll(mrc.context, stdoutEntry, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("failed to read standard output: %w", err)
	}
	var filenames []path.Component
	for filenameBytes := range bytes.SplitSeq(bytes.TrimSuffix(stdout, []byte{'\n'}), []byte{'\n'}) {
		filenameStr, valid := bytesToValidString(filenameBytes)
		if !valid {
			return nil, fmt.Errorf("invalid UTF-8 character sequences in filename %#v", filenameStr)
		}
		filename, ok := path.NewComponent(filenameStr)
		if !ok {
			return nil, fmt.Errorf("invalid filename %#v", filenameStr)
		}
		filenames = append(filenames, filename)
	}
	return filenames, nil
}

func (moduleOrRepositoryContext[TReference, TMetadata]) Realpath(p *model_starlark.BarePath) (*model_starlark.BarePath, error) {
	return nil, fmt.Errorf("TODO: Implement path.realpath(%#v)!", p.GetUNIXString())
}

// externalRepoAddingPathUnpackerInto is a decorator for
// UnpackerInto[*model_starlark.BarePath] that checks whether paths
// refer to ones belonging to external repositories. If they do, it
// ensures that the repo belonging to that path is added to the input
// root.
type externalRepoAddingPathUnpackerInto[TReference object.BasicReference, TMetadata BaseComputerReferenceMetadata] struct {
	context *moduleOrRepositoryContext[TReference, TMetadata]
	base    unpack.UnpackerInto[*model_starlark.BarePath]
}

func (ui *externalRepoAddingPathUnpackerInto[TReference, TMetadata]) maybeAddExternalRepo(bp *model_starlark.BarePath) error {
	mrc := ui.context
	if components := bp.GetRelativeTo(mrc.externalPath); len(components) >= 1 {
		return mrc.maybeAddExternalRepo(components[0])
	}
	return nil
}

func (ui *externalRepoAddingPathUnpackerInto[TReference, TMetadata]) UnpackInto(thread *starlark.Thread, v starlark.Value, dst **model_starlark.BarePath) error {
	if err := ui.base.UnpackInto(thread, v, dst); err != nil {
		return err
	}
	return ui.maybeAddExternalRepo(*dst)
}

func (ui *externalRepoAddingPathUnpackerInto[TReference, TMetadata]) Canonicalize(thread *starlark.Thread, v starlark.Value) (starlark.Value, error) {
	var bp *model_starlark.BarePath
	if err := ui.UnpackInto(thread, v, &bp); err != nil {
		return nil, err
	}
	if err := ui.maybeAddExternalRepo(bp); err != nil {
		return nil, err
	}
	return starlark.String(bp.GetUNIXString()), nil
}

func (ui *externalRepoAddingPathUnpackerInto[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return ui.base.GetConcatenationOperator()
}

// relativizeSymlinks is a post-processing pass that can be applied
// against the repo contents to eliminate any symbolic links that have
// absolute targets, or are relative and for which we can't trivially
// determine they don't escape the repo.
//
// This post-processing is necessary to ensure that the FileProperties
// function can expand symbolic links without depending on the stable
// input root path, or files stored on the host file system of the repo
// platform workers.
func (mrc *moduleOrRepositoryContext[TReference, TMetadata]) relativizeSymlinks(maximumEscapementLevels uint32) error {
	mrc.maybeGetDirectoryCreationParameters()
	if err := mrc.maybeGetStableInputRootPath(); err != nil {
		return err
	}
	if mrc.directoryLoadOptions == nil {
		return evaluation.ErrMissingDependency
	}

	dStack := util.NewNonEmptyStack(mrc.inputRootDirectory)
	var dPath *path.Trace
	for _, component := range mrc.subdirectoryComponents {
		var ok bool
		d, ok := dStack.Peek().directories[component]
		if !ok {
			return nil
		}
		dStack.Push(d)
		dPath = dPath.Append(component)
	}

	sr := changeTrackingDirectorySymlinksRelativizer[TReference, TMetadata]{
		context:                       mrc.context,
		environment:                   mrc.environment,
		directoryLoadOptions:          mrc.directoryLoadOptions,
		virtualRootScopeWalkerFactory: mrc.virtualRootScopeWalkerFactory,
	}
	return sr.relativizeSymlinksRecursively(dStack, dPath, maximumEscapementLevels)
}

func (c *baseComputer[TReference, TMetadata]) fetchModuleExtensionRepo(ctx context.Context, canonicalRepo label.CanonicalRepo, apparentRepo label.ApparentRepo, e RepoEnvironment[TReference, TMetadata]) (PatchedRepoValue[TMetadata], error) {
	// Obtain the definition of the declared repo.
	repoValue := e.GetModuleExtensionRepoValue(&model_analysis_pb.ModuleExtensionRepo_Key{
		CanonicalRepo: canonicalRepo.String(),
	})
	if !repoValue.IsSet() {
		return PatchedRepoValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	repo := repoValue.Message.Definition
	if repo == nil {
		return PatchedRepoValue[TMetadata]{}, errors.New("no repo definition present")
	}
	return c.fetchRepo(
		ctx,
		canonicalRepo,
		apparentRepo,
		model_core.Nested(repoValue, repo),
		e,
	)
}

type repositoryContext[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	moduleOrRepositoryContext *moduleOrRepositoryContext[TReference, TMetadata]
	canonicalRepo             label.CanonicalRepo
	rootRepoComponent         path.Component
	attrs                     starlark.Value
}

var (
	_ starlark.Value    = (*repositoryContext[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ starlark.HasAttrs = (*repositoryContext[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

func (repositoryContext[TReference, TMetadata]) String() string {
	return "<repository_ctx>"
}

func (repositoryContext[TReference, TMetadata]) Type() string {
	return "repository_ctx"
}

func (repositoryContext[TReference, TMetadata]) Freeze() {}

func (repositoryContext[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (repositoryContext[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("repository_ctx cannot be hashed")
}

func (rc *repositoryContext[TReference, TMetadata]) Attr(thread *starlark.Thread, name string) (starlark.Value, error) {
	mrc := rc.moduleOrRepositoryContext
	switch name {
	// Fields shared with module_ctx.
	case "download":
		return starlark.NewBuiltin("repository_ctx.download", mrc.doDownload), nil
	case "download_and_extract":
		return starlark.NewBuiltin("repository_ctx.download_and_extract", mrc.doDownloadAndExtract), nil
	case "execute":
		return starlark.NewBuiltin("repository_ctx.execute", mrc.doExecute), nil
	case "extract":
		return starlark.NewBuiltin("repository_ctx.extract", mrc.doExtract), nil
	case "file":
		return starlark.NewBuiltin("repository_ctx.file", mrc.doFile), nil
	case "getenv":
		return starlark.NewBuiltin("repository_ctx.getenv", mrc.doGetenv), nil
	case "os":
		mrc.maybeGetRepoPlatform()
		if !mrc.repoPlatform.IsSet() {
			return nil, evaluation.ErrMissingDependency
		}
		return newRepositoryOS[TReference, TMetadata](thread, mrc.repoPlatform.Message), nil
	case "path":
		return starlark.NewBuiltin("repository_ctx.path", mrc.doPath), nil
	case "read":
		return starlark.NewBuiltin("repository_ctx.read", mrc.doRead), nil
	case "report_progress":
		return starlark.NewBuiltin("repository_ctx.report_progress", mrc.doReportProgress), nil
	case "watch":
		return starlark.NewBuiltin("repository_ctx.watch", mrc.doWatch), nil
	case "which":
		return starlark.NewBuiltin("repository_ctx.which", mrc.doWhich), nil

	// Fields specific to repository_ctx.
	case "attr":
		return rc.attrs, nil
	case "delete":
		return starlark.NewBuiltin("repository_ctx.delete", rc.doDelete), nil
	case "name":
		return starlark.String(rc.canonicalRepo.String()), nil
	case "patch":
		return starlark.NewBuiltin("repository_ctx.patch", rc.doPatch), nil
	case "symlink":
		return starlark.NewBuiltin("repository_ctx.symlink", rc.doSymlink), nil
	case "template":
		return starlark.NewBuiltin("repository_ctx.template", rc.doTemplate), nil
	case "workspace_root":
		if err := mrc.maybeGetStableInputRootPath(); err != nil {
			return nil, err
		}
		if err := mrc.maybeAddExternalRepo(rc.rootRepoComponent); err != nil {
			return nil, err
		}
		return model_starlark.NewPath(mrc.externalPath.Append(rc.rootRepoComponent), mrc), nil
	}
	return nil, nil
}

func (repositoryContext[TReference, TMetadata]) AttrNames() []string {
	return []string{
		"attr",
		"delete",
		"download_and_extract",
		"download",
		"execute",
		"extract",
		"file",
		"getenv",
		"name",
		"os",
		"patch",
		"path",
		"read",
		"report_progress",
		"symlink",
		"template",
		"watch",
		"which",
		"workspace_root",
	}
}

func (rc *repositoryContext[TReference, TMetadata]) doDelete(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	mrc := rc.moduleOrRepositoryContext
	mrc.maybeGetDirectoryCreationParameters()
	if err := mrc.maybeGetStableInputRootPath(); err != nil {
		return nil, err
	}
	if mrc.directoryLoadOptions == nil {
		return nil, evaluation.ErrMissingDependency
	}

	var filePath *model_starlark.BarePath
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"path", unpack.Bind(thread, &filePath, mrc.pathUnpackerInto),
	); err != nil {
		return nil, err
	}

	r := &changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]{
		loadOptions: mrc.directoryLoadOptions,
		stack:       util.NewNonEmptyStack(mrc.inputRootDirectory),
	}
	if err := path.Resolve(filePath, mrc.virtualRootScopeWalkerFactory.New(r)); err != nil {
		if errors.Is(err, errDirectoryDoesNotExist) {
			return starlark.Bool(false), nil
		}
		return nil, fmt.Errorf("cannot resolve %#v: %w", filePath.GetUNIXString(), err)
	}
	if r.TerminalName == nil {
		return nil, fmt.Errorf("%#v does not resolve to a file", filePath.GetUNIXString())
	}

	d := r.stack.Peek()
	if err := d.maybeLoadContents(mrc.directoryLoadOptions); err != nil {
		return nil, err
	}

	if _, ok := d.directories[*r.TerminalName]; ok {
		delete(d.directories, *r.TerminalName)
		return starlark.Bool(true), nil
	}
	if _, ok := d.files[*r.TerminalName]; ok {
		delete(d.files, *r.TerminalName)
		return starlark.Bool(true), nil
	}
	if _, ok := d.symlinks[*r.TerminalName]; ok {
		delete(d.symlinks, *r.TerminalName)
		return starlark.Bool(true), nil
	}
	return starlark.Bool(false), nil
}

func (rc *repositoryContext[TReference, TMetadata]) doPatch(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	mrc := rc.moduleOrRepositoryContext
	mrc.maybeGetDirectoryCreationParameters()
	mrc.maybeGetFileReader()
	if err := mrc.maybeGetStableInputRootPath(); err != nil {
		return nil, err
	}
	if err := mrc.maybeInitializePatchedFiles(); err != nil {
		return nil, err
	}
	if mrc.directoryLoadOptions == nil || mrc.fileReader == nil {
		return nil, evaluation.ErrMissingDependency
	}

	var patchFile *model_starlark.BarePath
	var strip int
	var watchPatch string = "auto"
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"patch_file", unpack.Bind(thread, &patchFile, mrc.pathUnpackerInto),
		"strip?", unpack.Bind(thread, &strip, unpack.Int[int]()),
		"watch_patch?", unpack.Bind(thread, &watchPatch, unpack.String),
	); err != nil {
		return nil, err
	}

	// Resolve patch file from the stable root directory.
	r := &changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]{
		loadOptions: mrc.directoryLoadOptions,
		stack:       util.NewNonEmptyStack(mrc.inputRootDirectory),
	}
	if err := path.Resolve(patchFile, mrc.virtualRootScopeWalkerFactory.New(r)); err != nil {
		if errors.Is(err, errDirectoryDoesNotExist) {
			return starlark.Bool(false), nil
		}
		return nil, fmt.Errorf("cannot resolve %#v: %w", patchFile.GetUNIXString(), err)
	}
	trackedPatchFile, err := r.getFile()
	if err != nil {
		return nil, fmt.Errorf("%#v does not resolve to a file: %w", patchFile.GetUNIXString(), err)
	}

	// Resolve patch module directory as the "root" directory.
	repoDirectory, err := mrc.resolveRepoDirectory()
	if err != nil {
		return nil, err
	}

	// Apply the patch to the current repository.
	err = mrc.computer.applyPatch(
		mrc.context,
		repoDirectory,
		mrc.directoryLoadOptions,
		strip,
		mrc.fileReader,
		func() (io.Reader, error) {
			return trackedPatchFile.contents.openRead(mrc.context, mrc.fileReader, mrc.patchedFiles)
		},
		mrc.patchedFiles,
		mrc.patchedFilesWriter,
	)
	if err != nil {
		return nil, fmt.Errorf("cannot apply patch %q: %w", patchFile.GetUNIXString(), err)
	}
	return starlark.None, nil
}

func (rc *repositoryContext[TReference, TMetadata]) doSymlink(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	mrc := rc.moduleOrRepositoryContext
	mrc.maybeGetDirectoryCreationParameters()
	if err := mrc.maybeGetStableInputRootPath(); err != nil {
		return nil, err
	}
	if mrc.directoryLoadOptions == nil {
		return nil, evaluation.ErrMissingDependency
	}

	var target *model_starlark.BarePath
	var linkName *model_starlark.BarePath
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"target", unpack.Bind(thread, &target, mrc.pathUnpackerInto),
		"link_name", unpack.Bind(thread, &linkName, mrc.pathUnpackerInto),
	); err != nil {
		return nil, err
	}

	// Resolve path at which symlink needs
	// to be created.
	r := &changeTrackingDirectoryNewFileResolver[TReference, TMetadata]{
		loadOptions: mrc.directoryLoadOptions,
		stack:       util.NewNonEmptyStack(mrc.inputRootDirectory),
	}
	if err := path.Resolve(linkName, mrc.virtualRootScopeWalkerFactory.New(r)); err != nil {
		return nil, fmt.Errorf("cannot resolve %#v: %w", linkName.GetUNIXString(), err)
	}
	if r.TerminalName == nil {
		return nil, fmt.Errorf("%#v does not resolve to a file", linkName.GetUNIXString())
	}

	// Create symbolic link node.
	d := r.stack.Peek()
	if err := d.setSymlink(mrc.directoryLoadOptions, *r.TerminalName, target); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func (rc *repositoryContext[TReference, TMetadata]) doTemplate(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	mrc := rc.moduleOrRepositoryContext
	mrc.maybeGetDirectoryCreationParameters()
	mrc.maybeGetFileReader()
	stableInputRootPathError := mrc.maybeGetStableInputRootPath()
	if mrc.directoryCreationParameters == nil || mrc.fileReader == nil {
		return nil, evaluation.ErrMissingDependency
	}
	if stableInputRootPathError != nil {
		return nil, stableInputRootPathError
	}

	if len(args) > 4 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 4", b.Name(), len(args))
	}
	var filePath *model_starlark.BarePath
	var templatePath *model_starlark.BarePath
	var substitutions map[string]string
	executable := true
	var watchTemplate string
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"path", unpack.Bind(thread, &filePath, mrc.pathUnpackerInto),
		"template", unpack.Bind(thread, &templatePath, mrc.pathUnpackerInto),
		"substitutions?", unpack.Bind(thread, &substitutions, unpack.Dict(unpack.String, unpack.String)),
		"executable?", unpack.Bind(thread, &executable, unpack.Bool),
		"watch_template?", unpack.Bind(thread, &watchTemplate, unpack.String),
	); err != nil {
		return nil, err
	}

	needles := make([][]byte, 0, len(substitutions))
	replacements := make([][]byte, 0, len(substitutions))
	for _, needle := range slices.Sorted(maps.Keys(substitutions)) {
		needles = append(needles, []byte(needle))
		replacements = append(replacements, []byte(substitutions[needle]))
	}
	searchAndReplacer, err := search.NewMultiSearchAndReplacer(needles)
	if err != nil {
		return nil, fmt.Errorf("invalid substitution keys: %w", err)
	}

	// Load the template file.
	templateFileResolver := &changeTrackingDirectoryExistingFileResolver[TReference, TMetadata]{
		loadOptions: mrc.directoryLoadOptions,
		stack:       util.NewNonEmptyStack(mrc.inputRootDirectory),
	}
	if err := path.Resolve(templatePath, mrc.virtualRootScopeWalkerFactory.New(templateFileResolver)); err != nil {
		return nil, fmt.Errorf("cannot resolve template %#v: %w", templatePath.GetUNIXString(), err)
	}
	f, err := templateFileResolver.getFile()
	if err != nil {
		return nil, fmt.Errorf("cannot get file for template %#v: %w", templatePath.GetUNIXString(), err)
	}
	templateFile, err := f.contents.openRead(
		mrc.context,
		mrc.fileReader,
		mrc.patchedFiles,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to open template %#v: %w", templatePath.GetUNIXString(), err)
	}

	outputFileResolver := &changeTrackingDirectoryNewFileResolver[TReference, TMetadata]{
		loadOptions: mrc.directoryLoadOptions,
		stack:       util.NewNonEmptyStack(mrc.inputRootDirectory),
	}
	if err := path.Resolve(filePath, mrc.virtualRootScopeWalkerFactory.New(outputFileResolver)); err != nil {
		return nil, fmt.Errorf("cannot resolve %#v: %w", filePath.GetUNIXString(), err)
	}
	if outputFileResolver.TerminalName == nil {
		return nil, fmt.Errorf("%#v does not resolve to a file", filePath.GetUNIXString())
	}

	if err := mrc.maybeInitializePatchedFiles(); err != nil {
		return nil, err
	}

	// Perform substitutions.
	patchedFileOffsetBytes := mrc.patchedFilesWriter.GetOffsetBytes()
	if err := searchAndReplacer.SearchAndReplace(mrc.patchedFilesWriter, bufio.NewReader(templateFile), replacements); err != nil {
		return nil, fmt.Errorf("failed to write to file at %#v: %w", filePath.GetUNIXString(), err)
	}

	if err := outputFileResolver.stack.Peek().setFile(
		mrc.directoryLoadOptions,
		*outputFileResolver.TerminalName,
		&changeTrackingFile[TReference, TMetadata]{
			isExecutable: executable,
			contents: patchedFileContents[TReference, TMetadata]{
				offsetBytes: patchedFileOffsetBytes,
				sizeBytes:   mrc.patchedFilesWriter.GetOffsetBytes() - patchedFileOffsetBytes,
			},
		},
	); err != nil {
		return nil, err
	}

	return starlark.None, nil
}

func (c *baseComputer[TReference, TMetadata]) fetchRepo(ctx context.Context, canonicalRepo label.CanonicalRepo, apparentRepo label.ApparentRepo, repo model_core.Message[*model_starlark_pb.Repo_Definition, TReference], e RepoEnvironment[TReference, TMetadata]) (PatchedRepoValue[TMetadata], error) {
	// Obtain the definition of the repository rule used by the repo.
	rootModuleValue := e.GetRootModuleValue(&model_analysis_pb.RootModule_Key{})
	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	repoPlatform := e.GetRegisteredRepoPlatformValue(&model_analysis_pb.RegisteredRepoPlatform_Key{})
	repositoryRule, gotRepositoryRule := e.GetRepositoryRuleObjectValue(&model_analysis_pb.RepositoryRuleObject_Key{
		Identifier: repo.Message.RepositoryRuleIdentifier,
	})
	if !gotRepositoryRule || !allBuiltinsModulesNames.IsSet() || !repoPlatform.IsSet() || !rootModuleValue.IsSet() {
		return PatchedRepoValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	rootModuleName, err := label.NewModule(rootModuleValue.Message.RootModuleName)
	if err != nil {
		return PatchedRepoValue[TMetadata]{}, err
	}
	rootRepo := rootModuleName.ToModuleInstance(nil).GetBareCanonicalRepo()
	rootPackage := rootRepo.GetRootPackage()

	thread := c.newStarlarkThread(ctx, e, allBuiltinsModulesNames.Message.BuiltinsModuleNames)
	attrs := map[string]any{
		"name": starlark.String(canonicalRepo.String()),
	}

	var errIter error
	attrValues := maps.Collect(
		model_starlark.AllStructFields(
			ctx,
			c.valueReaders.List,
			model_core.Nested(repo, repo.Message.AttrValues),
			&errIter,
		),
	)
	if err != nil {
		return PatchedRepoValue[TMetadata]{}, err
	}

	for _, publicAttr := range repositoryRule.Attrs.Public {
		if value, ok := attrValues[publicAttr.Name]; ok {
			decodedValue, err := model_starlark.DecodeValue[TReference, TMetadata](
				value,
				/* currentIdentifier = */ nil,
				c.getValueDecodingOptions(ctx, func(resolvedLabel label.ResolvedLabel) (starlark.Value, error) {
					return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
				}),
			)
			if err != nil {
				return PatchedRepoValue[TMetadata]{}, err
			}

			// Determine the attribute type, so that the provided value can be canonicalized.
			canonicalizer := publicAttr.AttrType.GetCanonicalizer(rootPackage)
			canonicalizedValue, err := canonicalizer.Canonicalize(thread, decodedValue)
			if err != nil {
				return PatchedRepoValue[TMetadata]{}, fmt.Errorf("canonicalize attribute %#v: %w", publicAttr.Name, err)
			}
			attrs[publicAttr.Name] = canonicalizedValue
			delete(attrValues, publicAttr.Name)
		} else if d := publicAttr.Default; d != nil {
			attrs[publicAttr.Name] = d
		} else {
			return PatchedRepoValue[TMetadata]{}, fmt.Errorf("missing value for mandatory attribute %#v", publicAttr.Name)
		}
	}
	if len(attrValues) > 0 {
		return PatchedRepoValue[TMetadata]{}, fmt.Errorf("unknown attribute %#v", slices.Min(slices.Collect(maps.Keys(attrValues))))
	}

	for name, value := range repositoryRule.Attrs.Private {
		attrs[name] = value
	}

	// Invoke the implementation function.
	subdirectoryComponents := []path.Component{
		model_starlark.ComponentExternal,
		path.MustNewComponent(canonicalRepo.String()),
	}
	mrc, err := c.newModuleOrRepositoryContext(ctx, e, subdirectoryComponents)
	if err != nil {
		return PatchedRepoValue[TMetadata]{}, err
	}
	defer mrc.release()

	// These are needed at the end to create the directory Merkle tree.
	mrc.maybeGetDirectoryCreationParameters()
	mrc.maybeGetDirectoryReaders()
	mrc.maybeGetFileCreationParameters()

	_, err = starlark.Call(
		thread,
		repositoryRule.Implementation,
		/* args = */ starlark.Tuple{
			&repositoryContext[TReference, TMetadata]{
				moduleOrRepositoryContext: mrc,
				canonicalRepo:             canonicalRepo,
				rootRepoComponent:         path.MustNewComponent(rootRepo.String()),
				attrs:                     model_starlark.NewStructFromDict[TReference, TMetadata](nil, attrs),
			},
		},
		/* kwargs = */ nil,
	)
	if err != nil {
		var evalErr *starlark.EvalError
		if !errors.Is(err, evaluation.ErrMissingDependency) && errors.As(err, &evalErr) {
			return PatchedRepoValue[TMetadata]{}, errors.New(evalErr.Backtrace())
		}
		return PatchedRepoValue[TMetadata]{}, err
	}

	if err := mrc.relativizeSymlinks(1); err != nil {
		return PatchedRepoValue[TMetadata]{}, err
	}

	// Capture the resulting external/${repo} directory.
	repoDirectory, err := mrc.resolveRepoDirectory()
	if err != nil {
		return PatchedRepoValue[TMetadata]{}, err
	}

	if mrc.directoryCreationParameters == nil ||
		mrc.directoryReaders == nil ||
		mrc.fileCreationParameters == nil {
		return PatchedRepoValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return c.returnRepoMerkleTree(
		ctx,
		e,
		repoDirectory,
		mrc.directoryCreationParameters,
		mrc.directoryReaders,
		mrc.fileCreationParameters,
		mrc.patchedFiles,
	)
}

func (c *baseComputer[TReference, TMetadata]) createMerkleTreeFromChangeTrackingDirectory(
	ctx context.Context,
	e model_core.ObjectCapturer[TReference, TMetadata],
	rootDirectory *changeTrackingDirectory[TReference, TMetadata],
	directoryCreationParameters *model_filesystem.DirectoryCreationParameters,
	directoryReaders *DirectoryReaders[TReference],
	fileCreationParameters *model_filesystem.FileCreationParameters,
	patchedFiles io.ReaderAt,
) (model_core.PatchedMessage[*model_filesystem_pb.DirectoryReference, TMetadata], error) {
	if directory := rootDirectory.unmodifiedDirectory; directory.IsSet() {
		if contentsExternal, ok := directory.Message.GetContents().(*model_filesystem_pb.Directory_ContentsExternal); ok {
			// Directory remained completely unmodified.
			// Simply return the original directory.
			return model_core.Patch(e, model_core.Nested(directory, contentsExternal.ContentsExternal)), nil
		}
	}

	group, groupCtx := errgroup.WithContext(ctx)
	var createdRootDirectory model_filesystem.CreatedDirectory[TMetadata]
	group.Go(func() error {
		return model_filesystem.CreateDirectoryMerkleTree(
			groupCtx,
			semaphore.NewWeighted(1),
			group,
			directoryCreationParameters,
			&capturableChangeTrackingDirectory[TReference, TMetadata]{
				options: &capturableChangeTrackingDirectoryOptions[TReference, TMetadata]{
					context:                 groupCtx,
					directoryContentsReader: directoryReaders.DirectoryContents,
					leavesReader:            directoryReaders.Leaves,
					fileCreationParameters:  fileCreationParameters,
					fileMerkleTreeCapturer:  model_filesystem.NewSimpleFileMerkleTreeCapturer(e),
					patchedFiles:            patchedFiles,
					objectCapturer:          e,
				},
				directory: rootDirectory,
			},
			model_filesystem.NewSimpleDirectoryMerkleTreeCapturer(e),
			&createdRootDirectory,
		)
	})
	if err := group.Wait(); err != nil {
		return model_core.PatchedMessage[*model_filesystem_pb.DirectoryReference, TMetadata]{}, err
	}

	// Store the root directory itself. We don't embed it into the
	// response, as that prevents it from being accessed separately.
	createdRootDirectoryObject, err := model_core.MarshalAndEncode(
		model_core.ProtoToBinaryMarshaler(createdRootDirectory.Message),
		c.referenceFormat,
		directoryCreationParameters.GetEncoder(),
	)
	if err != nil {
		return model_core.PatchedMessage[*model_filesystem_pb.DirectoryReference, TMetadata]{}, err
	}

	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_filesystem_pb.DirectoryReference, error) {
		directoryReference, err := patcher.CaptureAndAddDecodableReference(ctx, createdRootDirectoryObject, e)
		if err != nil {
			return nil, err
		}
		return createdRootDirectory.ToDirectoryReference(directoryReference), nil
	})
}

func (c *baseComputer[TReference, TMetadata]) returnRepoMerkleTree(
	ctx context.Context,
	e model_core.ObjectCapturer[TReference, TMetadata],
	rootDirectory *changeTrackingDirectory[TReference, TMetadata],
	directoryCreationParameters *model_filesystem.DirectoryCreationParameters,
	directoryReaders *DirectoryReaders[TReference],
	fileCreationParameters *model_filesystem.FileCreationParameters,
	patchedFiles io.ReaderAt,
) (PatchedRepoValue[TMetadata], error) {
	rootDirectoryReference, err := c.createMerkleTreeFromChangeTrackingDirectory(ctx, e, rootDirectory, directoryCreationParameters, directoryReaders, fileCreationParameters, patchedFiles)
	if err != nil {
		return PatchedRepoValue[TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_analysis_pb.Repo_Value{
			RootDirectoryReference: rootDirectoryReference.Message,
		},
		rootDirectoryReference.Patcher,
	), nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeRepoValue(ctx context.Context, key *model_analysis_pb.Repo_Key, e RepoEnvironment[TReference, TMetadata]) (PatchedRepoValue[TMetadata], error) {
	canonicalRepo, err := label.NewCanonicalRepo(key.CanonicalRepo)
	if err != nil {
		return PatchedRepoValue[TMetadata]{}, fmt.Errorf("invalid canonical repo: %w", err)
	}

	if _, _, ok := canonicalRepo.GetModuleExtension(); ok {
		return c.fetchModuleExtensionRepo(ctx, canonicalRepo, canonicalRepo.GetModuleInstance().GetModule().ToApparentRepo(), e)
	} else {
		moduleInstance := canonicalRepo.GetModuleInstance()
		if _, ok := moduleInstance.GetModuleVersion(); !ok {
			// See if this is one of the modules for which sources
			// are provided. If so, return a repo value immediately.
			// This allows any files contained within to be accessed
			// without processing MODULE.bazel. This prevents cyclic
			// dependencies.
			buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
			if !buildSpecification.IsSet() {
				return PatchedRepoValue[TMetadata]{}, evaluation.ErrMissingDependency
			}

			// Check to see if the client overrode this module manually.
			moduleName := moduleInstance.GetModule().String()
			modules := buildSpecification.Message.Modules
			if i, ok := sort.Find(
				len(modules),
				func(i int) int { return strings.Compare(moduleName, modules[i].Name) },
			); ok {
				// Found matching module.
				rootDirectoryReference := model_core.Patch(e, model_core.Nested(buildSpecification, modules[i].RootDirectoryReference))
				return model_core.NewPatchedMessage(
					&model_analysis_pb.Repo_Value{
						RootDirectoryReference: rootDirectoryReference.Message,
					},
					rootDirectoryReference.Patcher,
				), nil
			}

			// Check to see if there is a MODULE.bazel override for this module.
			var singleVersionOverridePatchLabels, singleVersionOverridePatchCommands []string
			var singleVersionOverridePatchStrip int
			remoteOverridesValue := e.GetModulesWithRemoteOverridesValue(&model_analysis_pb.ModulesWithRemoteOverrides_Key{})
			if !remoteOverridesValue.IsSet() {
				return PatchedRepoValue[TMetadata]{}, evaluation.ErrMissingDependency
			}
			remoteOverrides := remoteOverridesValue.Message.ModuleOverrides
			if i := sort.Search(
				len(remoteOverrides),
				func(i int) bool { return remoteOverrides[i].Name >= moduleName },
			); i < len(remoteOverrides) && remoteOverrides[i].Name == moduleName {
				// Found the remote override
				remoteOverride := remoteOverrides[i]
				switch override := remoteOverride.Kind.(type) {
				case *model_analysis_pb.ModuleOverride_RepositoryRule:
					return c.fetchRepo(
						ctx,
						canonicalRepo,
						canonicalRepo.GetModuleInstance().GetModule().ToApparentRepo(),
						model_core.Nested(remoteOverridesValue, override.RepositoryRule),
						e,
					)
				case *model_analysis_pb.ModuleOverride_SingleVersion_:
					if override.SingleVersion.Version != "" {
						return PatchedRepoValue[TMetadata]{}, fmt.Errorf("TODO: single version override with exact version should skip Minimal Version Selection!")
					}
					singleVersionOverridePatchLabels = override.SingleVersion.PatchLabels
					singleVersionOverridePatchCommands = override.SingleVersion.PatchCommands
					singleVersionOverridePatchStrip = int(override.SingleVersion.PatchStrip)
				default:
					// TODO: Implement Archive, SingleVersion, MultipleVersions
					return PatchedRepoValue[TMetadata]{}, fmt.Errorf("remote override type for %q: %w", remoteOverride.Name, errors.ErrUnsupported)
				}
			}

			// If a version of the module is selected as
			// part of the final build list, we can download
			// that exact version.
			buildListValue := e.GetModuleFinalBuildListValue(&model_analysis_pb.ModuleFinalBuildList_Key{})
			if !buildListValue.IsSet() {
				return PatchedRepoValue[TMetadata]{}, evaluation.ErrMissingDependency
			}
			buildList := buildListValue.Message.BuildList
			if i, ok := sort.Find(
				len(buildList),
				func(i int) int { return strings.Compare(moduleName, buildList[i].Name) },
			); ok {
				return c.fetchModuleFromRegistry(
					ctx,
					buildList[i],
					e,
					singleVersionOverridePatchLabels,
					singleVersionOverridePatchCommands,
					singleVersionOverridePatchStrip,
				)
			}
		}
	}

	return PatchedRepoValue[TMetadata]{}, errors.New("repo not found")
}
