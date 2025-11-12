package initialsizeclass

import (
	"time"

	model_initialsizeclass "bonanza.build/pkg/proto/model/initialsizeclass"
)

type smallestSizeClassStrategyCalculator struct{}

func (smallestSizeClassStrategyCalculator) GetStrategies(perSizeClassStatsMap map[uint32]*model_initialsizeclass.PerSizeClassStats, sizeClasses []uint32, originalTimeout time.Duration) []Strategy {
	if len(sizeClasses) <= 1 {
		return nil
	}
	return []Strategy{
		{
			Probability:                1.0,
			ForegroundExecutionTimeout: originalTimeout,
		},
	}
}

func (smallestSizeClassStrategyCalculator) GetBackgroundExecutionTimeout(perSizeClassStatsMap map[uint32]*model_initialsizeclass.PerSizeClassStats, sizeClasses []uint32, sizeClassIndex int, originalTimeout time.Duration) time.Duration {
	panic("Background execution should not be performed")
}

// SmallestSizeClassStrategyCalculator implements a StrategyCalculator
// that always prefers running actions on the smallest size class.
//
// This StrategyCalculator behaves similar to FallbackAnalyzer, with the
// main difference that it still causes execution times and outcomes to
// be tracked in storage.
var SmallestSizeClassStrategyCalculator StrategyCalculator = smallestSizeClassStrategyCalculator{}
