package starlark

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// FeatureSet contains the features that are enabled and disabled for a
// target. Features can be set on the command line through --features,
// in REPO.bazel through repo(features = ...), in BUILD files through
// package(features = ...), and on targets through the "features" attr.
type FeatureSet struct {
	enabled  map[string]struct{}
	disabled map[string]struct{}
}

// NewFeatureSet creates a FeatureSet from lists of enabled and
// disabled features, as returned by GetEnabled() and GetDisabled().
// Both lists must be sorted, contain no duplicates, and be disjoint.
func NewFeatureSet(enabled, disabled []string) (FeatureSet, error) {
	fs := FeatureSet{
		enabled:  make(map[string]struct{}, len(enabled)),
		disabled: make(map[string]struct{}, len(disabled)),
	}
	for i, name := range enabled {
		if i > 0 && enabled[i-1] >= name {
			return FeatureSet{}, fmt.Errorf("enabled features are not sorted and deduplicated: %#v is followed by %#v", enabled[i-1], name)
		}
		fs.enabled[name] = struct{}{}
	}
	for i, name := range disabled {
		if i > 0 && disabled[i-1] >= name {
			return FeatureSet{}, fmt.Errorf("disabled features are not sorted and deduplicated: %#v is followed by %#v", disabled[i-1], name)
		}
		if _, ok := fs.enabled[name]; ok {
			return FeatureSet{}, fmt.Errorf("feature %#v is both enabled and disabled", name)
		}
		fs.disabled[name] = struct{}{}
	}
	return fs, nil
}

// ParseFeatures parses a list of features, as provided to --features,
// repo(), package(), or the "features" attr. Features prefixed with
// "-" are disabled. If a feature is both enabled and disabled,
// disabling it takes precedence.
func ParseFeatures(features []string) FeatureSet {
	fs := FeatureSet{
		enabled:  map[string]struct{}{},
		disabled: map[string]struct{}{},
	}
	for _, feature := range features {
		if name, ok := strings.CutPrefix(feature, "-"); ok {
			fs.disabled[name] = struct{}{}
		} else {
			fs.enabled[feature] = struct{}{}
		}
	}
	for name := range fs.disabled {
		delete(fs.enabled, name)
	}
	return fs
}

// Merge features that are set at a more specific level (e.g., the
// package) on top of features set at a coarser level (e.g., the
// command line). Features enabled or disabled at the more specific
// level override the coarser level.
func (fs FeatureSet) Merge(fine FeatureSet) FeatureSet {
	merged := FeatureSet{
		enabled:  maps.Clone(fs.enabled),
		disabled: maps.Clone(fs.disabled),
	}
	if merged.enabled == nil {
		merged.enabled = map[string]struct{}{}
	}
	if merged.disabled == nil {
		merged.disabled = map[string]struct{}{}
	}
	for name := range fine.enabled {
		merged.enabled[name] = struct{}{}
		delete(merged.disabled, name)
	}
	for name := range fine.disabled {
		merged.disabled[name] = struct{}{}
		delete(merged.enabled, name)
	}
	return merged
}

// GetEnabled returns the names of the features that are enabled,
// sorted alphabetically.
func (fs FeatureSet) GetEnabled() []string {
	return slices.Sorted(maps.Keys(fs.enabled))
}

// GetDisabled returns the names of the features that are disabled,
// sorted alphabetically.
func (fs FeatureSet) GetDisabled() []string {
	return slices.Sorted(maps.Keys(fs.disabled))
}
