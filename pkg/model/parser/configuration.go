package parser

import (
	model_parser_cfg_pb "bonanza.build/pkg/proto/configuration/model/parser"

	"github.com/buildbarn/bb-storage/pkg/eviction"
	"github.com/buildbarn/bb-storage/pkg/util"
)

func NewParsedObjectPoolFromConfiguration(configuration *model_parser_cfg_pb.ParsedObjectPool) (*ParsedObjectPool, error) {
	if configuration == nil {
		// Don't perform any caching of objects.
		return nil, nil
	}
	evictionSet, err := eviction.NewSetFromConfiguration[ParsedObjectEvictionKey](configuration.CacheReplacementPolicy)
	if err != nil {
		return nil, util.StatusWrap(err, "Failed to create eviction set")
	}
	return NewParsedObjectPool(evictionSet, int(configuration.Count), int(configuration.SizeBytes)), nil
}
