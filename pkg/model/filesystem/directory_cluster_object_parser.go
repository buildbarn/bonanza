package filesystem

import (
	"unique"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// DirectoryCluster is a list of all Directory messages that are
// contained in a single object in storage. Directories are stored in
// topological order, meaning that the root directory is located at
// index zero.
//
// The advantage of a DirectoryCluster over a plain Directory message is
// that it's possible to refer to individual directories using an index.
type DirectoryCluster []Directory

// Directory contained in a DirectoryCluster.
type Directory struct {
	Directory *model_filesystem_pb.DirectoryContents

	// Indices at which child directories with inline contents are
	// accessible within the same cluster. If the child directory
	// has external contents, the index is set to -1.
	ChildDirectoryIndices []int
}

type directoryClusterObjectParser[TReference any] struct{}

// NewDirectoryClusterObjectParser creates an ObjectParser that is
// capable of parsing directory objects, and exposing them in the form
// of a DirectoryCluster.
func NewDirectoryClusterObjectParser[TReference any]() model_parser.MessageObjectParser[TReference, DirectoryCluster] {
	return &directoryClusterObjectParser[TReference]{}
}

func (directoryClusterObjectParser[TReference]) ParseObject(in model_core.Message[[]byte, TReference], decodingParameters []byte) (model_core.Message[DirectoryCluster, TReference], error) {
	if len(decodingParameters) > 0 {
		return model_core.Message[DirectoryCluster, TReference]{}, status.Error(codes.InvalidArgument, "Unexpected decoding parameters")
	}

	var d model_filesystem_pb.DirectoryContents
	if err := proto.Unmarshal(in.Message, &d); err != nil {
		return model_core.Message[DirectoryCluster, TReference]{}, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to parse directory")
	}

	// Recursively visit all Directory messages contained in the
	// object and store them in a list. This allows the caller to
	// address each directory separately.
	var cluster DirectoryCluster
	_, err := addDirectoriesToCluster(
		&cluster,
		model_core.NewMessage(&d, in.OutgoingReferences),
		nil,
	)
	if err != nil {
		return model_core.Message[DirectoryCluster, TReference]{}, err
	}
	return model_core.Nested(in, cluster), nil
}

type directoryClusterObjectParserKey struct{}

var directoryClusterObjectParserKeyHandle = unique.Make[any](directoryClusterObjectParserKey{})

func (directoryClusterObjectParser[TReference]) AppendUniqueKeys(keys []unique.Handle[any]) []unique.Handle[any] {
	return append(keys, directoryClusterObjectParserKeyHandle)
}

func (directoryClusterObjectParser[TReference]) GetDecodingParametersSizeBytes() int {
	return 0
}

func addDirectoriesToCluster[TReference any](c *DirectoryCluster, d model_core.Message[*model_filesystem_pb.DirectoryContents, TReference], dTrace *path.Trace) (int, error) {
	directoryIndex := len(*c)
	childDirectoryIndices := make([]int, len(d.Message.Directories))
	*c = append(
		*c,
		Directory{
			Directory:             d.Message,
			ChildDirectoryIndices: childDirectoryIndices,
		},
	)

	for i, entry := range d.Message.Directories {
		switch contents := entry.Directory.GetContents().(type) {
		case *model_filesystem_pb.Directory_ContentsExternal:
			childDirectoryIndices[i] = -1
		case *model_filesystem_pb.Directory_ContentsInline:
			// Subdirectory is stored in the same object.
			// Recurse into it, so that it gets its own
			// directory index.
			name, ok := path.NewComponent(entry.Name)
			if !ok {
				return 0, status.Errorf(codes.InvalidArgument, "Entry %#v in directory %#v has an invalid name", entry.Name, dTrace.GetUNIXString())
			}
			childDirectoryIndex, err := addDirectoriesToCluster(
				c,
				model_core.Nested(d, contents.ContentsInline),
				dTrace.Append(name),
			)
			if err != nil {
				return 0, err
			}
			childDirectoryIndices[i] = childDirectoryIndex
		default:
			// Subdirectory is stored in another object.
			name, ok := path.NewComponent(entry.Name)
			if !ok {
				return 0, status.Errorf(codes.InvalidArgument, "Entry %#v in directory %#v has an invalid name", entry.Name, dTrace.GetUNIXString())
			}
			return 0, status.Errorf(codes.InvalidArgument, "Directory %#v has no contents", dTrace.Append(name).GetUNIXString())
		}
	}
	return directoryIndex, nil
}
