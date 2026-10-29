package analysis_test

import (
	"encoding"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

// packageGroupContainsEnvironment implements PackageGroupContainsEnvironment
// on top of the mock that is used for testing QueryExpansion, which
// provides all other methods that are needed.
type packageGroupContainsEnvironment struct {
	*MockQueryExpansionEnvironmentForTesting
}

func (packageGroupContainsEnvironment) GetPackageGroupContainsValue(key *model_analysis_pb.PackageGroupContains_Key) model_core.Message[*model_analysis_pb.PackageGroupContains_Value, model_core.CreatedObjectTree] {
	panic("package groups don't include other package groups")
}

func TestPackageGroupContains(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	newOverridesObject := func(build func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_starlark_pb.PackageGroup_Package) model_core.Decodable[model_core.CreatedObject[model_core.CreatedObjectTree]] {
		return newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
			return model_core.NewProtoBinaryMarshaler(&model_starlark_pb.PackageGroup_Subpackages_Overrides{
				Packages: build(patcher),
			})
		})
	}

	t.Run("ExternalOverrides", func(t *testing.T) {
		// Overrides that are too large to be stored inline are
		// stored in separate objects, which may be nested. This
		// package group contains @@main+//foo and everything
		// below it.
		e := packageGroupContainsEnvironment{NewMockQueryExpansionEnvironmentForTesting(ctrl)}
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: "@@main+//:group"}),
		).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.Target_Value {
			return &model_analysis_pb.Target_Value{
				Definition: &model_starlark_pb.Target_Definition{
					Kind: &model_starlark_pb.Target_Definition_PackageGroup{
						PackageGroup: &model_starlark_pb.PackageGroup{
							Tree: &model_starlark_pb.PackageGroup_Subpackages{
								Overrides: &model_starlark_pb.PackageGroup_Subpackages_OverridesExternal{
									OverridesExternal: attachObject(patcher, newOverridesObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_starlark_pb.PackageGroup_Package {
										return []*model_starlark_pb.PackageGroup_Package{{
											Component: "main+",
											Subpackages: &model_starlark_pb.PackageGroup_Subpackages{
												Overrides: &model_starlark_pb.PackageGroup_Subpackages_OverridesExternal{
													OverridesExternal: attachObject(patcher, newOverridesObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_starlark_pb.PackageGroup_Package {
														return []*model_starlark_pb.PackageGroup_Package{{
															Component:      "foo",
															IncludePackage: true,
															Subpackages: &model_starlark_pb.PackageGroup_Subpackages{
																IncludeSubpackages: true,
															},
														}}
													})),
												},
											},
										}}
									})),
								},
							},
						},
					},
				},
			}
		})).AnyTimes()

		for packageName, expected := range map[string]bool{
			"@@main+":            false,
			"@@main+//bar":       false,
			"@@main+//foo":       true,
			"@@main+//foo/bar":   true,
			"@@main+//foobar":    false,
			"@@other+//foo":      false,
			"@@other+//foo/test": false,
		} {
			value, err := bct.computer.ComputePackageGroupContainsValue(ctx, &model_analysis_pb.PackageGroupContains_Key{
				PackageGroupLabel: "@@main+//:group",
				Package:           packageName,
			}, e)
			require.NoError(t, err, packageName)
			require.Equal(t, expected, value.Message.Contains, packageName)
		}
	})
}
