package initialsizeclass

import (
	pb "bonanza.build/pkg/proto/configuration/scheduler"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewAnalyzerFromConfiguration creates a new initial size class
// analyzer based on options provided in a configuration file.
func NewAnalyzerFromConfiguration(
	configuration *pb.InitialSizeClassAnalyzerConfiguration,
	previousExecutionStatsStore PreviousExecutionStatsStore,
	previousExecutionStatsCommonKeyDataReference object.LocalReference,
	previousExecutionStatsDecodingParametersSizeBytes int,
) (Analyzer, error) {
	if configuration == nil {
		return nil, status.Error(codes.InvalidArgument, "No initial size class analyzer configuration provided")
	}

	maximumExecutionTimeout := configuration.GetMaximumExecutionTimeout()
	if err := maximumExecutionTimeout.CheckValid(); err != nil {
		return nil, util.StatusWrap(err, "Invalid maximum execution timeout")
	}
	actionTimeoutExtractor := NewActionTimeoutExtractor(maximumExecutionTimeout.AsDuration())

	analyzer := NewFallbackAnalyzer(actionTimeoutExtractor)
	if fdConfiguration := configuration.GetFeedbackDriven(); fdConfiguration != nil {
		if previousExecutionStatsStore == nil {
			return nil, status.Error(codes.InvalidArgument, "Feedback driven analysis can only be enabled if a PreviousExecutionStats store is configured")
		}
		failureCacheDuration := fdConfiguration.GetFailureCacheDuration()
		if err := failureCacheDuration.CheckValid(); err != nil {
			return nil, util.StatusWrap(err, "Invalid failure cache duration")
		}

		strategyCalculator := SmallestSizeClassStrategyCalculator
		if pageRankConfiguration := fdConfiguration.GetPageRank(); pageRankConfiguration != nil {
			minimumExecutionTimeout := pageRankConfiguration.GetMinimumExecutionTimeout()
			if err := minimumExecutionTimeout.CheckValid(); err != nil {
				return nil, util.StatusWrap(err, "Invalid minimum acceptable execution time")
			}
			strategyCalculator = NewPageRankStrategyCalculator(
				minimumExecutionTimeout.AsDuration(),
				pageRankConfiguration.GetAcceptableExecutionTimeIncreaseExponent(),
				pageRankConfiguration.GetSmallerSizeClassExecutionTimeoutMultiplier(),
				pageRankConfiguration.GetMaximumConvergenceError(),
			)
		}

		analyzer = NewFeedbackDrivenAnalyzer(
			analyzer,
			previousExecutionStatsStore,
			previousExecutionStatsCommonKeyDataReference,
			previousExecutionStatsDecodingParametersSizeBytes,
			random.NewFastSingleThreadedGenerator(),
			clock.SystemClock,
			actionTimeoutExtractor,
			failureCacheDuration.AsDuration(),
			strategyCalculator,
			int(fdConfiguration.GetHistorySize()),
		)
	}
	return analyzer, nil
}
