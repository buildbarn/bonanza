package executewithstorage

import (
	"context"
	"crypto/ecdh"
	"iter"

	model_core "bonanza.build/pkg/model/core"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	"bonanza.build/pkg/remoteexecution"

	"github.com/buildbarn/bb-storage/pkg/util"
)

type objectExportingClient[TInternal, TExternal any] struct {
	base     remoteexecution.Client[*Action[TExternal], *model_core.Decodable[TExternal], *model_core.Decodable[TExternal]]
	exporter model_core.ObjectExporter[TInternal, TExternal]
}

// NewObjectExportingClient creates a decorator for the remote execution
// client that converts references of actions, execution events and
// results from one reference format to another.
//
// This decorator can be used to force flushing of objects that only
// reside in local memory to storage, so that the worker executing the
// action has access to them as well.
func NewObjectExportingClient[TInternal, TExternal any](
	base remoteexecution.Client[*Action[TExternal], *model_core.Decodable[TExternal], *model_core.Decodable[TExternal]],
	exporter model_core.ObjectExporter[TInternal, TExternal],
) remoteexecution.Client[*Action[TInternal], *model_core.Decodable[TInternal], *model_core.Decodable[TInternal]] {
	return &objectExportingClient[TInternal, TExternal]{
		base:     base,
		exporter: exporter,
	}
}

func (c *objectExportingClient[TInternal, TExternal]) RunAction(
	ctx context.Context,
	platformECDHPublicKey *ecdh.PublicKey,
	action *Action[TInternal],
	actionAdditionalData *encryptedaction_pb.Action_AdditionalData,
	resultReferenceOut **model_core.Decodable[TInternal],
	errOut *error,
) iter.Seq[remoteexecution.ExecutionUpdate[*model_core.Decodable[TInternal]]] {
	var externalActionReference *model_core.Decodable[TExternal]
	if action.Reference != nil {
		reference, err := c.exporter.ExportReference(ctx, action.Reference.Value)
		if err != nil {
			*errOut = util.StatusWrap(err, "Failed to export action")
			return func(yield func(remoteexecution.ExecutionUpdate[*model_core.Decodable[TInternal]]) bool) {}
		}
		decodableReference := model_core.CopyDecodable(*action.Reference, reference)
		externalActionReference = &decodableReference
	}

	ctxWithCancel, cancel := context.WithCancel(ctx)
	var baseResultReference *model_core.Decodable[TExternal]
	var baseErr error
	eventReferences := c.base.RunAction(
		ctxWithCancel,
		platformECDHPublicKey,
		&Action[TExternal]{
			Reference: externalActionReference,
			Encoders:  action.Encoders,
			Format:    action.Format,
		},
		actionAdditionalData,
		&baseResultReference,
		&baseErr,
	)
	return func(yield func(remoteexecution.ExecutionUpdate[*model_core.Decodable[TInternal]]) bool) {
		defer cancel()

		// Convert event references to native types.
		for baseUpdate := range eventReferences {
			update, _ := remoteexecution.MapExecutionUpdateEvent(
				baseUpdate,
				func(eventReference *model_core.Decodable[TExternal]) (*model_core.Decodable[TInternal], error) {
					return c.importReference(eventReference), nil
				},
			)
			if !yield(update) {
				break
			}
		}

		// Convert result reference to native type.
		if baseErr != nil {
			*errOut = baseErr
			return
		}
		*resultReferenceOut = c.importReference(baseResultReference)
		*errOut = nil
	}
}

// importReference converts an execution event or result reference that
// may be absent to its native type.
func (c *objectExportingClient[TInternal, TExternal]) importReference(reference *model_core.Decodable[TExternal]) *model_core.Decodable[TInternal] {
	if reference == nil {
		return nil
	}
	importedReference := model_core.CopyDecodable(*reference, c.exporter.ImportReference(reference.Value))
	return &importedReference
}
