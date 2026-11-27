package analysis

import (
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

// newTargetVariant creates a TargetVariant message, given the
// configuration in which a target is analyzed and the aspects that are
// applied to it. If the configuration is empty and no aspects are
// applied, nil is returned. This ensures that keys of analysis
// functions containing TargetVariant messages are encoded identically.
func newTargetVariant(configurationReference *model_core_pb.DecodableReference, aspects []*model_starlark_pb.AppliedAspect) *model_starlark_pb.TargetVariant {
	if configurationReference == nil && len(aspects) == 0 {
		return nil
	}
	return model_starlark_pb.TargetVariant_builder{
		ConfigurationReference: configurationReference,
		Aspects:                aspects,
	}.Build()
}
