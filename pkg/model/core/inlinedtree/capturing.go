package inlinedtree

import (
	model_core "bonanza.build/pkg/model/core"
)

// CapturedParentAppender is a simplified version of ParentAppender that
// receives an already captured object, as opposed to receiving the
// literal contents of the created object. This is sufficient for most
// candidates.
type CapturedParentAppender[TParentMessage any, TMetadata model_core.ReferenceMetadata] func(
	parent model_core.PatchedMessage[TParentMessage, TMetadata],
	externalObject *model_core.Decodable[model_core.MetadataEntry[TMetadata]],
)

// Capturing converts a CapturedParentAppender to a plain
// ParentAppender.
func Capturing[TParentMessage any, TMetadata model_core.ReferenceMetadata](
	capturer model_core.CreatedObjectCapturer[TMetadata],
	appender CapturedParentAppender[TParentMessage, TMetadata],
) ParentAppender[TParentMessage, TMetadata] {
	return func(
		parent model_core.PatchedMessage[TParentMessage, TMetadata],
		externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
	) {
		if externalObject == nil {
			// Inline the message.
			appender(parent, nil)
		} else {
			// Store the message in an external object.
			capturedObject := model_core.CopyDecodable(
				*externalObject,
				externalObject.Value.Capture(capturer),
			)
			appender(parent, &capturedObject)
		}
	}
}
