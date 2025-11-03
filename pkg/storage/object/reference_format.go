package object

import (
	"crypto/sha256"
	"encoding/binary"

	"bonanza.build/pkg/encoding/float16"
	"bonanza.build/pkg/proto/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReferenceFormat describes the algorithm to use to compute the
// LocalReference of an object. Right now only a single algorithm is
// supported, called "SHA256_V1".
type ReferenceFormat struct{}

// SHA256V1ReferenceFormat is a predeclared instance of ReferenceFormat
// that uses algorithm "SHA256_V1".
var SHA256V1ReferenceFormat = ReferenceFormat{}

// NewReferenceFormat converts a ReferenceFormat enumeration value that
// is used as part of gRPC requests to a native type. Right now it
// merely validates that the provided enumeration value corresponds to
// SHA256_V1.
func NewReferenceFormat(value object.ReferenceFormat_Value) (ReferenceFormat, error) {
	if value != object.ReferenceFormat_SHA256_V1 {
		return ReferenceFormat{}, status.Error(codes.InvalidArgument, "This implementation only supports reference format SHA256_V1")
	}
	return ReferenceFormat{}, nil
}

// ToProto converts a ReferenceFormat to an enumeration value that can
// be embedded into gRPC request messages.
func (ReferenceFormat) ToProto() object.ReferenceFormat_Value {
	return object.ReferenceFormat_SHA256_V1
}

// GetReferenceSizeBytes returns the size in bytes of references encoded
// in this format.
func (ReferenceFormat) GetReferenceSizeBytes() int {
	return referenceSizeBytes
}

// NewLocalReference converts a local reference that is stored in binary
// format to an in-memory representation. It also validates that all
// fields contained in the reference are within bounds.
func (ReferenceFormat) NewLocalReference(rawReference []byte) (r LocalReference, err error) {
	// Construct the reference.
	if len(rawReference) != referenceSizeBytes {
		return LocalReference{}, status.Errorf(
			codes.InvalidArgument,
			"Reference is %d bytes in size, while SHA256_V1 references are %d bytes in size",
			len(rawReference),
			referenceSizeBytes,
		)
	}
	r.rawReference = *(*[referenceSizeBytes]byte)(rawReference)

	sizeBytes := r.GetSizeBytes()
	if sizeBytes < minimumObjectSizeBytes || sizeBytes > maximumObjectSizeBytes {
		return LocalReference{}, status.Errorf(
			codes.InvalidArgument,
			"Size is %d bytes, which lies outside the permitted range of [%d, %d] bytes",
			sizeBytes,
			minimumObjectSizeBytes,
			maximumObjectSizeBytes,
		)
	}

	// Perform validation against the reference's fields.
	maximumDegree := sizeBytes / referenceSizeBytes
	degree := r.GetDegree()
	if degree > maximumDegree {
		return LocalReference{}, status.Errorf(
			codes.InvalidArgument,
			"Degree is %d, while an object of %d bytes in size can only have a maximum degree of %d",
			degree,
			sizeBytes,
			maximumDegree,
		)
	}

	height := r.GetHeight()
	if (degree > 0) != (height > 0) {
		return LocalReference{}, status.Errorf(
			codes.InvalidArgument,
			"Degree is %d and height is %d, while both either have to be zero or non-zero",
			degree,
			height,
		)
	}

	if bounds, parentsSizeBytes := maximumTotalParentsSizeBytesBounds[height], binary.LittleEndian.Uint16(rawReference[38:]); parentsSizeBytes < bounds.minimum || parentsSizeBytes > bounds.maximum {
		return LocalReference{}, status.Errorf(
			codes.InvalidArgument,
			"Maximum total parents size is %d bytes, which at height %d lies outside the permitted range of [%d, %d] bytes",
			float16.ToUint64(parentsSizeBytes),
			height,
			float16.ToUint64(bounds.minimum),
			float16.ToUint64(bounds.maximum),
		)
	}

	return r, err
}

// NewFlatReference converts a flat reference that is stored in binary
// format to an in-memory representation. It also validates that all
// fields contained in the reference are within bounds.
func (ReferenceFormat) NewFlatReference(rawReference []byte) (r FlatReference, err error) {
	// Construct the reference.
	if len(rawReference) != SHA256V1FlatReferenceSizeBytes {
		return FlatReference{}, status.Errorf(
			codes.InvalidArgument,
			"Reference is %d bytes in size, while SHA256_V1 flat references are %d bytes in size",
			len(rawReference),
			SHA256V1FlatReferenceSizeBytes,
		)
	}
	r.rawReference = *(*[SHA256V1FlatReferenceSizeBytes]byte)(rawReference)

	sizeBytes := r.GetSizeBytes()
	if sizeBytes < minimumObjectSizeBytes || sizeBytes > maximumObjectSizeBytes {
		return FlatReference{}, status.Errorf(
			codes.InvalidArgument,
			"Size is %d bytes, which lies outside the permitted range of [%d, %d] bytes",
			sizeBytes,
			minimumObjectSizeBytes,
			maximumObjectSizeBytes,
		)
	}

	return r, err
}

// GetMaximumObjectSizeBytes returns the maximum size of objects encoded
// in this format. This size is guaranteed to be small enough to permit
// safe transmission of such objects as part of gRPC requests and
// responses, and to instantiate the object in memory of a reasonably
// modern computer.
func (ReferenceFormat) GetMaximumObjectSizeBytes() int {
	return maximumObjectSizeBytes
}

// NewContents creates a new object having the provided set of outgoing
// references and data payload. It is assumed that outgoing references
// are provided in sorted order.
func (ReferenceFormat) NewContents(outgoingReferences []LocalReference, payload []byte) (*Contents, error) {
	sizeBytes := len(outgoingReferences)*referenceSizeBytes + len(payload)
	if sizeBytes < minimumObjectSizeBytes || sizeBytes > maximumObjectSizeBytes {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"Size is %d bytes, which lies outside the permitted range of [%d, %d] bytes",
			sizeBytes,
			minimumObjectSizeBytes,
			maximumObjectSizeBytes,
		)
	}

	var data []byte
	var rcs referenceStatsComputer
	if len(outgoingReferences) == 0 {
		data = payload
	} else {
		data = make([]byte, 0, sizeBytes)
		for _, outgoingReference := range outgoingReferences {
			if err := rcs.addChildReference(outgoingReference); err != nil {
				return nil, err
			}
			data = append(data, outgoingReference.GetRawReference()...)
		}
		data = append(data, payload...)
	}

	var rawReference [referenceSizeBytes]byte
	*(*[32]byte)(rawReference[:]) = sha256.Sum256(data)
	binary.LittleEndian.PutUint32(rawReference[32:], uint32(sizeBytes))
	*(*[5]byte)(rawReference[35:]) = rcs.getStats()

	return &Contents{
		LocalReference: LocalReference{rawReference: rawReference},
		data:           data,
	}, nil
}
