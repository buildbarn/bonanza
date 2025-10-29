package virtual

import (
	"bytes"
	"io"

	"bonanza.build/pkg/encoding/varint"
	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
	"github.com/buildbarn/bb-storage/pkg/util"
)

type resolvableHandleAllocatingFileFactory struct {
	FileFactory
	handleAllocator virtual.ResolvableHandleAllocator
	errorLogger     util.ErrorLogger
}

// NewResolvableHandleAllocatingFileFactory creates a decorator for
// FileFactory that annotates all files with a resolvable handle. This
// can be used by tools that provide read-only and direct access to all
// files in storage, similar to bb_clientd's "cas" directory.
func NewResolvableHandleAllocatingFileFactory(base FileFactory, handleAllocation virtual.ResolvableHandleAllocation, errorLogger util.ErrorLogger) FileFactory {
	ff := &resolvableHandleAllocatingFileFactory{
		FileFactory: base,
		errorLogger: errorLogger,
	}
	ff.handleAllocator = handleAllocation.AsResolvableAllocator(ff.resolveHandle)
	return ff
}

func (ff *resolvableHandleAllocatingFileFactory) resolveHandle(r io.ByteReader) (virtual.DirectoryChild, virtual.Status) {
	endBytes, err := varint.ReadForward[uint64](r)
	if err != nil {
		return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
	}

	var reference model_core.Decodable[object.LocalReference]
	if endBytes > 0 {
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
		decodingParametersSizeBytes := ff.FileFactory.GetDecodingParametersSizeBytes(localReference.GetHeight() > 0)
		decodingParameters := make([]byte, 0, decodingParametersSizeBytes)
		for i := 0; i < int(decodingParametersSizeBytes); i++ {
			b, err := r.ReadByte()
			if err != nil {
				return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
			}
			decodingParameters = append(decodingParameters, b)
		}
		reference, err = model_core.NewDecodable(localReference, decodingParameters)
		if err != nil {
			return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
		}
	}

	b, err := r.ReadByte()
	if err != nil {
		return virtual.DirectoryChild{}, virtual.StatusErrBadHandle
	}
	isExecutable := b != 0x00

	f, err := ff.LookupFile(
		model_filesystem.FileContentsEntry[object.LocalReference]{
			EndBytes:  endBytes,
			Reference: reference,
		},
		isExecutable,
	)
	if err != nil {
		ff.errorLogger.Log(util.StatusWrap(err, "Failed to look up file"))
		return virtual.DirectoryChild{}, virtual.StatusErrIO
	}
	return virtual.DirectoryChild{}.FromLeaf(f), virtual.StatusOK
}

func computeFileID(fileContents model_filesystem.FileContentsEntry[object.LocalReference], isExecutable bool) io.WriterTo {
	handle := varint.AppendForward(nil, fileContents.EndBytes)
	if fileContents.EndBytes > 0 {
		handle = varint.AppendForward(handle, fileContents.Reference.Value.GetReferenceFormat().ToProto())
		handle = append(handle, fileContents.Reference.Value.GetRawReference()...)
		handle = append(handle, fileContents.Reference.GetDecodingParameters()...)
	}
	if isExecutable {
		handle = append(handle, 0x01)
	} else {
		handle = append(handle, 0x00)
	}
	return bytes.NewBuffer(handle)
}

func (ff *resolvableHandleAllocatingFileFactory) LookupFile(fileContents model_filesystem.FileContentsEntry[object.LocalReference], isExecutable bool) (virtual.LinkableLeaf, error) {
	f, err := ff.FileFactory.LookupFile(fileContents, isExecutable)
	if err != nil {
		return nil, err
	}
	return ff.handleAllocator.New(computeFileID(fileContents, isExecutable)).AsLinkableLeaf(f), nil
}
