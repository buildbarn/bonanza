package routing

import (
	pb "bonanza.build/pkg/proto/configuration/scheduler"
	"bonanza.build/pkg/scheduler/initialsizeclass"
	"bonanza.build/pkg/scheduler/invocation"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewActionRouterFromConfiguration creates an ActionRouter based on
// options specified in a configuration file.
func NewActionRouterFromConfiguration(
	configuration *pb.ActionRouterConfiguration,
	previousExecutionStatsStore initialsizeclass.PreviousExecutionStatsStore,
	previousExecutionStatsCommonKeyDataReference object.LocalReference,
	previousExecutionStatsDecodingParametersSizeBytes int,
) (ActionRouter, error) {
	if configuration == nil {
		return nil, status.Error(codes.InvalidArgument, "No action router configuration provided")
	}
	switch kind := configuration.Kind.(type) {
	case *pb.ActionRouterConfiguration_Simple:
		invocationKeyExtractors := make([]invocation.KeyExtractor, 0, len(kind.Simple.InvocationKeyExtractors))
		for i, entry := range kind.Simple.InvocationKeyExtractors {
			invocationKeyExtractor, err := invocation.NewKeyExtractorFromConfiguration(entry)
			if err != nil {
				return nil, util.StatusWrapf(err, "Failed to create invocation key extractor at index %d", i)
			}
			invocationKeyExtractors = append(invocationKeyExtractors, invocationKeyExtractor)
		}
		initialSizeClassAnalyzer, err := initialsizeclass.NewAnalyzerFromConfiguration(
			kind.Simple.InitialSizeClassAnalyzer,
			previousExecutionStatsStore,
			previousExecutionStatsCommonKeyDataReference,
			previousExecutionStatsDecodingParametersSizeBytes,
		)
		if err != nil {
			return nil, util.StatusWrap(err, "Failed to create initial size class analyzer")
		}
		return NewSimpleActionRouter(invocationKeyExtractors, initialSizeClassAnalyzer), nil
	default:
		return nil, status.Error(codes.InvalidArgument, "Configuration did not contain a supported action router type")
	}
}
