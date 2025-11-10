package object

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"

	"bonanza.build/pkg/encoding/float16"
	"bonanza.build/pkg/proto/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Contents of an object read from storage, or to be written to storage.
type Contents struct {
	LocalReference
	data []byte
}

var (
	_ BasicReference                     = (*Contents)(nil)
	_ OutgoingReferences[LocalReference] = (*Contents)(nil)
)

// NewContentsFromFullData constructs object contents from raw data that
// is read from disk or an incoming RPC message. Construction fails if
// the provided data does not match the expected reference.
func NewContentsFromFullData(reference LocalReference, data []byte) (*Contents, error) {
	c := &Contents{
		LocalReference: reference,
		data:           data,
	}

	if expectedSizeBytes := reference.GetSizeBytes(); len(c.data) != expectedSizeBytes {
		return nil, status.Errorf(codes.InvalidArgument, "Data is %d bytes in size, while %d bytes were expected", len(c.data), expectedSizeBytes)
	}
	actualHash := sha256.Sum256(c.data)
	if expectedHash := reference.GetHash(); !bytes.Equal(actualHash[:], expectedHash) {
		return nil, status.Errorf(codes.InvalidArgument, "Data has SHA-256 hash %s, while %s was expected", hex.EncodeToString(actualHash[:]), hex.EncodeToString(expectedHash))
	}

	if err := c.validateOutgoingReferences(); err != nil {
		return nil, err
	}
	return c, nil
}

// MustNewContents constructs object contents given a list of outgoing
// references and a payload. This function may be used as part of tests.
func MustNewContents(referenceFormatValue object.ReferenceFormat_Value, references []LocalReference, payload []byte) *Contents {
	referenceFormat := util.Must(NewReferenceFormat(referenceFormatValue))
	contents, err := referenceFormat.NewContents(references, payload)
	if err != nil {
		panic(err)
	}
	return contents
}

// GetOutgoingReference returns one of the outgoing references that is
// part of this object.
func (c *Contents) GetOutgoingReference(i int) LocalReference {
	outgoingReferences := c.data[:c.GetDegree()*SHA256V1ReferenceSizeBytes]
	return LocalReference{
		rawReference: *(*[SHA256V1ReferenceSizeBytes]byte)(outgoingReferences[i*SHA256V1ReferenceSizeBytes:]),
	}
}

// DetachOutgoingReferences copies all of the outgoing references into
// an OutgoingReferencesList, allowing the object contents to be garbage
// collected while keeping the outgoing references available.
func (c *Contents) DetachOutgoingReferences() OutgoingReferences[LocalReference] {
	degree := c.GetDegree()
	l := make(OutgoingReferencesList[LocalReference], 0, degree)
	for i := 0; i < degree; i++ {
		l = append(l, LocalReference{
			rawReference: *(*[SHA256V1ReferenceSizeBytes]byte)(c.data[i*SHA256V1ReferenceSizeBytes:]),
		})
	}
	return l
}

// GetFullData returns the full object contents, including the binary
// representation of outgoing references that are stored at the
// beginning. This method can be used to encode contents for
// transmission across the network, or writing them to disk.
func (c *Contents) GetFullData() []byte {
	return c.data
}

// GetPayload returns the payload of the object, not including the
// outgoing references that are stored at the beginning.
func (c *Contents) GetPayload() []byte {
	return c.data[c.GetDegree()*SHA256V1ReferenceSizeBytes:]
}

func (c *Contents) cloneWithReference(r LocalReference) *Contents {
	if r == c.LocalReference {
		return c
	}
	return &Contents{
		LocalReference: r,
		data:           c.data,
	}
}

// FlattenContents flattens the reference of the object, so that its
// height and degree are both zero.
//
// This is used by the read caching backend, where we want to cache
// individual objects as opposed to graphs. When writing objects to the
// local cache, we set the height and degree to zero, so that there is
// no need to track any leases.
func (c *Contents) FlattenContents() *Contents {
	return c.cloneWithReference(c.LocalReference.Flatten().GetLocalReference())
}

func (c *Contents) validateOutgoingReferences() error {
	var rcs referenceStatsComputer
	degree := c.GetDegree()
	referenceFormat := c.GetReferenceFormat()
	for i := 0; i < degree; i++ {
		outgoingReference, err := referenceFormat.NewLocalReference(c.data[i*SHA256V1ReferenceSizeBytes : (i+1)*SHA256V1ReferenceSizeBytes])
		if err != nil {
			return util.StatusWrapf(err, "Invalid reference at index %d", i)
		}
		if err := rcs.addChildReference(outgoingReference); err != nil {
			return err
		}
	}

	if expectedHeight := c.GetHeight(); rcs.height != expectedHeight {
		return status.Errorf(codes.InvalidArgument, "Object has height %d, while %d was expected", rcs.height, expectedHeight)
	}

	actualMaximumTotalParentsSizeBytes, ok := float16.FromUint64RoundUp(uint64(rcs.maximumTotalParentsSizeBytes))
	if !ok {
		panic("maximum total parents size should be computable without overflow")
	}
	if expectedMaximumTotalParentsSizeBytes := c.getRawMaximumTotalParentsSizeBytes(); actualMaximumTotalParentsSizeBytes != expectedMaximumTotalParentsSizeBytes {
		return status.Errorf(
			codes.InvalidArgument,
			"Object has a maximum total parents size of %d bytes, while %d bytes were expected",
			float16.ToUint64(actualMaximumTotalParentsSizeBytes),
			float16.ToUint64(expectedMaximumTotalParentsSizeBytes),
		)
	}
	return nil
}

// Unflatten the reference of the object, so that its height and degree
// are set to the desired value.
//
// This is used by the read caching backend, where we want to cache
// individual objects as opposed to graphs. When reading objects from
// the local cache, their heights and degrees will be set to zero. This
// method can be used to undo this transformation.
func (c *Contents) Unflatten(newReference LocalReference) (*Contents, error) {
	if *(*[35]byte)(newReference.rawReference[:]) != *(*[35]byte)(c.LocalReference.rawReference[:]) {
		return nil, status.Error(codes.InvalidArgument, "Hash and size of flattened and unflattened references do not match")
	}
	cFlat := c.cloneWithReference(newReference)
	if err := cFlat.validateOutgoingReferences(); err != nil {
		return nil, err
	}
	return cFlat, nil
}

type referenceStatsComputer struct {
	lastRawReference             *[SHA256V1ReferenceSizeBytes]byte
	height                       int
	degree                       int
	maximumTotalParentsSizeBytes int
}

func (rcs *referenceStatsComputer) addChildReference(outgoingReference LocalReference) error {
	rawReference := &outgoingReference.rawReference
	if rcs.lastRawReference != nil && bytes.Compare(rcs.lastRawReference[:], rawReference[:]) >= 0 {
		return status.Errorf(codes.InvalidArgument, "Outgoing references at indices %d and %d are not properly sorted", rcs.degree-1, rcs.degree)
	}
	rcs.lastRawReference = rawReference

	childHeight := outgoingReference.GetHeight()
	if newHeight := childHeight + 1; rcs.height < newHeight {
		if newHeight > math.MaxUint8 {
			return status.Errorf(codes.InvalidArgument, "Outgoing reference at index %d has height %d, which is too high", rcs.degree, childHeight)
		}
		rcs.height = newHeight
	}

	rcs.degree++

	if newMaximumTotalParentsSizeBytes := outgoingReference.GetMaximumTotalParentsSizeBytes(true); rcs.maximumTotalParentsSizeBytes < newMaximumTotalParentsSizeBytes {
		rcs.maximumTotalParentsSizeBytes = newMaximumTotalParentsSizeBytes
	}
	return nil
}

func (rcs *referenceStatsComputer) getStats() (stats [5]byte) {
	stats[0] = byte(rcs.height)
	binary.LittleEndian.PutUint16(stats[1:], uint16(rcs.degree))
	f16, ok := float16.FromUint64RoundUp(uint64(rcs.maximumTotalParentsSizeBytes))
	if !ok {
		panic("maximumTotalParentsSizeBytes is too large")
	}
	binary.LittleEndian.PutUint16(stats[3:], f16)
	return stats
}
