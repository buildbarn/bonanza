package analysis

import (
	"context"
	"errors"

	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeBuildSpecificationValue(ctx context.Context, key *model_analysis_pb.BuildSpecification_Key, e BuildSpecificationEnvironment[TReference, TMetadata]) (PatchedBuildSpecificationValue[TMetadata], error) {
	return PatchedBuildSpecificationValue[TMetadata]{}, errors.New("no build specification provided")
}
