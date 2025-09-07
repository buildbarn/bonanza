package analysis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"bonanza.build/pkg/evaluation"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

type packageGroupContainsEnvironment[TReference any] interface {
	GetPackageGroupContainsValue(key *model_analysis_pb.PackageGroupContains_Key) model_core.Message[*model_analysis_pb.PackageGroupContains_Value, TReference]
}

func packageGroupContains[TReference any](e packageGroupContainsEnvironment[TReference], packageGroup model_core.Message[*model_starlark_pb.PackageGroup, TReference], fromPackage label.CanonicalPackage) (bool, error) {
	// Check whether the provided package is directly contained in
	// this package group.
	subpackages := model_core.Nested(packageGroup, packageGroup.Message.Tree)
	component := fromPackage.GetCanonicalRepo().String()
	fromPackagePath := fromPackage.GetPackagePath()
	for {
		// Determine whether there are any overrides present at
		// this level in the tree.
		var overrides model_core.Message[*model_starlark_pb.PackageGroup_Subpackages_Overrides, TReference]
		switch o := subpackages.Message.GetOverrides().(type) {
		case *model_starlark_pb.PackageGroup_Subpackages_OverridesInline:
			overrides = model_core.Nested(subpackages, o.OverridesInline)
		case *model_starlark_pb.PackageGroup_Subpackages_OverridesExternal:
			return false, errors.New("TODO: Download external overrides!")
		case nil:
			// No overrides present.
		default:
			return false, errors.New("invalid overrides type")
		}

		packages := overrides.Message.GetPackages()
		packageIndex, ok := sort.Find(
			len(packages),
			func(i int) int { return strings.Compare(component, packages[i].Component) },
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
		subpackages = model_core.Nested(overrides, p.Subpackages)

		if fromPackagePath == "" {
			// Fully resolved the package name. Consider
			// include_package.
			if !p.IncludePackage {
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
	for _, includePackageGroup := range packageGroup.Message.IncludePackageGroups {
		containsValue := e.GetPackageGroupContainsValue(&model_analysis_pb.PackageGroupContains_Key{
			PackageGroupLabel: includePackageGroup,
			Package:           fromPackage.String(),
		})
		if !containsValue.IsSet() {
			return false, evaluation.ErrMissingDependency
		}
		if containsValue.Message.Contains {
			return true, nil
		}
	}
	return false, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputePackageGroupContainsValue(ctx context.Context, key *model_analysis_pb.PackageGroupContains_Key, e PackageGroupContainsEnvironment[TReference, TMetadata]) (PatchedPackageGroupContainsValue[TMetadata], error) {
	fromPackage, err := label.NewCanonicalPackage(key.Package)
	if err != nil {
		return PatchedPackageGroupContainsValue[TMetadata]{}, fmt.Errorf("invalid from package: %w", err)
	}

	targetValue := e.GetTargetValue(&model_analysis_pb.Target_Key{
		Label: key.PackageGroupLabel,
	})
	if !targetValue.IsSet() {
		return PatchedPackageGroupContainsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	definition, ok := targetValue.Message.Definition.GetKind().(*model_starlark_pb.Target_Definition_PackageGroup)
	if !ok {
		return PatchedPackageGroupContainsValue[TMetadata]{}, errors.New("target is not a package group")
	}

	contains, err := packageGroupContains(e, model_core.Nested(targetValue, definition.PackageGroup), fromPackage)
	if err != nil {
		return PatchedPackageGroupContainsValue[TMetadata]{}, err
	}
	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_analysis_pb.PackageGroupContains_Value{
			Contains: contains,
		},
	), nil
}
