package buffered

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_tag "bonanza.build/pkg/model/tag"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/clock"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Reference of an object whose contents may be buffered in memory.
type Reference struct {
	object.LocalReference
	embeddedMetadata ReferenceMetadata
}

// ReferenceMetadata of an object whose contents may or may not have
// been written to storage yet.
type ReferenceMetadata struct {
	contents *object.Contents
	children []ReferenceMetadata
}

// Discard the contents of an object which may or may not have been
// written to storage yet.
func (ReferenceMetadata) Discard() {}

type objectManager struct{}

// NewObjectManager creates an object manager that is capable of
// capturing and referencing objects, potentially buffering their
// contents in memory before flushing them to storage. This permits code
// that creates objects to continue running, flushing objects
// asynchronously.
//
// TODO: The current implementation is just a placeholder that does not
// actually flush objects asynchronously.
func NewObjectManager() model_core.ObjectManager[Reference, ReferenceMetadata] {
	return objectManager{}
}

func (objectManager) CaptureCreatedObject(ctx context.Context, createdObject model_core.CreatedObject[ReferenceMetadata]) (ReferenceMetadata, error) {
	return ReferenceMetadata{
		contents: createdObject.Contents,
		children: createdObject.Metadata,
	}, nil
}

func (objectManager) CaptureExistingObject(reference Reference) ReferenceMetadata {
	if reference.embeddedMetadata.contents != nil {
		return reference.embeddedMetadata
	}
	return ReferenceMetadata{}
}

func (objectManager) ReferenceObject(capturedObject model_core.MetadataEntry[ReferenceMetadata]) Reference {
	return Reference{
		LocalReference:   capturedObject.LocalReference,
		embeddedMetadata: capturedObject.Metadata,
	}
}

type objectContentsWalker struct {
	embeddedMetadata ReferenceMetadata
}

func (w objectContentsWalker) GetContents(ctx context.Context) (*object.Contents, []dag.ObjectContentsWalker, error) {
	m := &w.embeddedMetadata
	if m.contents == nil {
		return nil, nil, status.Error(codes.Internal, "Contents for this object are not available for upload, as this object was expected to already exist")
	}

	walkers := make([]dag.ObjectContentsWalker, 0, len(m.children))
	for _, child := range m.children {
		walkers = append(walkers, objectContentsWalker{
			embeddedMetadata: child,
		})
	}
	return m.contents, walkers, nil
}

func (objectContentsWalker) Discard() {}

type objectExporter struct {
	dagUploader dag.Uploader[struct{}, object.LocalReference]
}

// NewObjectExporter creates an object exporter that accepts references
// of created objects that may or may not have been flushed to storage
// yet. As objects are flushed to storage asynchronously regardless of
// ExportReference() being called, this implementation merely waits for
// the flushing to complete.
//
// TODO: The current implementation is just a placeholder that does not
// actually flush objects asynchronously.
func NewObjectExporter(dagUploader dag.Uploader[struct{}, object.LocalReference]) model_core.ObjectExporter[Reference, object.LocalReference] {
	return &objectExporter{
		dagUploader: dagUploader,
	}
}

func (oe *objectExporter) ExportReference(ctx context.Context, internalReference Reference) (object.LocalReference, error) {
	err := oe.dagUploader.UploadDAG(
		ctx,
		internalReference.LocalReference,
		objectContentsWalker{
			embeddedMetadata: internalReference.embeddedMetadata,
		},
	)
	if err != nil {
		var badReference object.LocalReference
		return badReference, nil
	}
	return internalReference.LocalReference, nil
}

func (objectExporter) ImportReference(externalReference object.LocalReference) Reference {
	return Reference{LocalReference: externalReference}
}

type objectReader struct {
	base model_parser.ObjectReader[object.LocalReference, model_core.Message[[]byte, object.LocalReference]]
}

// NewObjectReader creates a decorator for ObjectReader that makes it
// accept buffered references. If the object to be read is already
// present in memory, the contents are returned directly, as opposed to
// actually issuing a read from storage.
func NewObjectReader(
	base model_parser.ObjectReader[object.LocalReference, model_core.Message[[]byte, object.LocalReference]],
) model_parser.ObjectReader[Reference, model_core.Message[[]byte, Reference]] {
	return &objectReader{
		base: base,
	}
}

func (r *objectReader) ReadObject(ctx context.Context, reference Reference) (model_core.Message[[]byte, Reference], error) {
	if contents := reference.embeddedMetadata.contents; contents != nil {
		// Object has not been written to storage yet.
		// Return the copy that lives in memory.
		//
		// TODO: We should return some kind of hint to indicate
		// that the caller is not permitted to cache this!
		degree := contents.GetDegree()
		outgoingReferences := make(object.OutgoingReferencesList[Reference], 0, degree)
		children := reference.embeddedMetadata.children
		for i := range degree {
			outgoingReferences = append(outgoingReferences, Reference{
				LocalReference:   contents.GetOutgoingReference(i),
				embeddedMetadata: children[i],
			})
		}
		return model_core.NewMessage(contents.GetPayload(), outgoingReferences), nil
	}

	// Read object from storage.
	m, err := r.base.ReadObject(ctx, reference.GetLocalReference())
	if err != nil {
		return model_core.Message[[]byte, Reference]{}, err
	}

	degree := m.OutgoingReferences.GetDegree()
	outgoingReferences := make(object.OutgoingReferencesList[Reference], 0, degree)
	for i := range degree {
		outgoingReferences = append(outgoingReferences, Reference{
			LocalReference: m.OutgoingReferences.GetOutgoingReference(i),
		})
	}
	return model_core.NewMessage(m.Message, outgoingReferences), nil
}

type tagBoundUpdater struct {
	dagUploader dag.Uploader[struct{}, object.LocalReference]
	privateKey  ed25519.PrivateKey
	publicKey   [ed25519.PublicKeySize]byte
	clock       clock.Clock
}

// NewTagBoundUpdater creates an updater for tags in storage that
// accepts buffered references. It automatically flushes objects that
// are only present locally to storage prior to creating the tag.
func NewTagBoundUpdater(dagUploader dag.Uploader[struct{}, object.LocalReference], privateKey ed25519.PrivateKey, clock clock.Clock) model_tag.BoundUpdater[Reference] {
	return &tagBoundUpdater{
		dagUploader: dagUploader,
		privateKey:  privateKey,
		publicKey:   *(*[ed25519.PublicKeySize]byte)(privateKey.Public().(ed25519.PublicKey)),
		clock:       clock,
	}
}

func (u *tagBoundUpdater) UpdateTag(ctx context.Context, keyHash [sha256.Size]byte, reference Reference) error {
	key := tag.Key{
		SignaturePublicKey: u.publicKey,
		Hash:               keyHash,
	}
	value := tag.Value{
		Reference: reference.GetLocalReference(),
		Timestamp: u.clock.Now(),
	}
	signedValue, err := value.Sign(u.privateKey, keyHash)
	if err != nil {
		return err
	}
	return u.dagUploader.UploadTaggedDAG(
		ctx,
		struct{}{},
		key,
		signedValue,
		objectContentsWalker{
			embeddedMetadata: reference.embeddedMetadata,
		},
	)
}
