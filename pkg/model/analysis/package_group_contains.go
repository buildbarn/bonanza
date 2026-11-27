package analysis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

// packageGroupContains returns whether a package is contained in a
// package group, either directly or through one of the package groups
// that it includes.
func (*baseComputer[TReference, TMetadata]) packageGroupContains(ctx context.Context, e PackageGroupContainsValueGetter[TReference], valueReaders *model_starlark.ValueReaders[TReference], packageGroup model_core.Message[*model_starlark_pb.PackageGroup, TReference], fromPackage label.CanonicalPackage) (bool, error) {
	// Check whether the provided package is directly contained in
	// this package group.
	subpackages := model_core.Nested(packageGroup, packageGroup.Message.GetTree())
	component := fromPackage.GetCanonicalRepo().String()
	fromPackagePath := fromPackage.GetPackagePath()
	for {
		// Determine whether there are any overrides present at
		// this level in the tree.
		var overrides model_core.Message[*model_starlark_pb.PackageGroup_Subpackages_Overrides, TReference]
		switch subpackages.Message.WhichOverrides() {
		case model_starlark_pb.PackageGroup_Subpackages_OverridesInline_case:
			overrides = model_core.Nested(subpackages, subpackages.Message.GetOverridesInline())
		case model_starlark_pb.PackageGroup_Subpackages_OverridesExternal_case:
			// Overrides were too large to be stored inline.
			var err error
			overrides, err = model_parser.Dereference(ctx, valueReaders.PackageGroupOverrides, model_core.Nested(subpackages, subpackages.Message.GetOverridesExternal()))
			if err != nil {
				return false, fmt.Errorf("failed to load overrides: %w", err)
			}
		case 0:
			// No overrides present.
		default:
			return false, errors.New("invalid overrides type")
		}

		packages := overrides.Message.GetPackages()
		packageIndex, ok := sort.Find(
			len(packages),
			func(i int) int { return strings.Compare(component, packages[i].GetComponent()) },
		)
		if !ok {
			// No override is in place for this specific
			// component. Consider include_subpackages.
			if !subpackages.Message.GetIncludeSubpackages() {
				break
			}
			return true, nil
		}

		// An override is in place for this specific component.
		// Continue traversal.
		p := packages[packageIndex]
		subpackages = model_core.Nested(overrides, p.GetSubpackages())

		if fromPackagePath == "" {
			// Fully resolved the package name. Consider
			// include_package.
			if !p.GetIncludePackage() {
				break
			}
			return true, nil
		}

		// Extract the next component.
		if split := strings.IndexByte(fromPackagePath, '/'); split < 0 {
			component = fromPackagePath
			fromPackagePath = ""
		} else {
			component = fromPackagePath[:split]
			fromPackagePath = fromPackagePath[split+1:]
		}
	}

	// Package is not directly contained in this package group.
	// Check package groups that are being included.
	for _, includePackageGroup := range packageGroup.Message.GetIncludePackageGroups() {
		containsValue := e.GetPackageGroupContainsValue(&model_analysis_pb.PackageGroupContains_Key{
			PackageGroupLabel: includePackageGroup,
			Package:           fromPackage.String(),
		})
		if !containsValue.IsSet() {
			return false, evaluation.ErrMissingDependency
		}
		if containsValue.Message.GetContains() {
			return true, nil
		}
	}
	return false, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputePackageGroupContainsValue(ctx context.Context, key *model_analysis_pb.PackageGroupContains_Key, e PackageGroupContainsEnvironment[TReference, TMetadata]) (PatchedPackageGroupContainsValue[TMetadata], error) {
	valueReaders, gotValueReaders := getStarlarkValueReaders(e)

	fromPackage, err := label.NewCanonicalPackage(key.GetPackage())
	if err != nil {
		return PatchedPackageGroupContainsValue[TMetadata]{}, fmt.Errorf("invalid from package: %w", err)
	}

	targetValue := e.GetTargetValue(&model_analysis_pb.Target_Key{
		Label: key.GetPackageGroupLabel(),
	})
	if !targetValue.IsSet() || !gotValueReaders {
		return PatchedPackageGroupContainsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	targetValueDefinition := targetValue.Message.GetDefinition()
	if targetValueDefinition == nil {
		return PatchedPackageGroupContainsValue[TMetadata]{}, errors.New("package group does not exist")
	}
	definition := targetValueDefinition.GetPackageGroup()
	if definition == nil {
		return PatchedPackageGroupContainsValue[TMetadata]{}, errors.New("target is not a package group")
	}

	contains, err := c.packageGroupContains(ctx, e, valueReaders, model_core.Nested(targetValue, definition), fromPackage)
	if err != nil {
		return PatchedPackageGroupContainsValue[TMetadata]{}, err
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.PackageGroupContains_Value{
		Contains: contains,
	}), nil
}
