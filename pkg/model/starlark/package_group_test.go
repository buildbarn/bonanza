package starlark_test

import (
	"testing"

	"bonanza.build/pkg/label"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestNewPackageGroupFromVisibility(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)

	encoder := NewMockBinaryEncoder(ctrl)
	encoder.EXPECT().GetDecodingParametersSizeBytes().Return(0).AnyTimes()

	t.Run("private", func(t *testing.T) {
		t.Run("Success", func(t *testing.T) {
			packageGroup, err := model_starlark.NewPackageGroupFromVisibility(
				ctx,
				[]label.ResolvedLabel{
					util.Must(label.NewResolvedLabel("@@foo+//visibility:private")),
				},
				encoder,
				&inlinedtree.Options{
					ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
					MaximumSizeBytes: 0,
				},
				NewMockObjectManagerForTesting(ctrl),
			)
			require.NoError(t, err)
			testutil.RequireEqualProto(t, &model_starlark_pb.PackageGroup{
				Tree: &model_starlark_pb.PackageGroup_Subpackages{},
			}, packageGroup.Message)
		})

		t.Run("Duplicate", func(t *testing.T) {
			_, err := model_starlark.NewPackageGroupFromVisibility(
				ctx,
				[]label.ResolvedLabel{
					util.Must(label.NewResolvedLabel("@@foo+//visibility:private")),
					util.Must(label.NewResolvedLabel("@@foo+//visibility:private")),
				},
				encoder,
				&inlinedtree.Options{
					ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
					MaximumSizeBytes: 0,
				},
				NewMockObjectManagerForTesting(ctrl),
			)
			require.EqualError(t, err, "//visibility:private may not be combined with other labels")
		})
	})

	t.Run("public", func(t *testing.T) {
		t.Run("Success", func(t *testing.T) {
			packageGroup, err := model_starlark.NewPackageGroupFromVisibility(
				ctx,
				[]label.ResolvedLabel{
					util.Must(label.NewResolvedLabel("@@foo+//visibility:public")),
				},
				encoder,
				&inlinedtree.Options{
					ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
					MaximumSizeBytes: 0,
				},
				NewMockObjectManagerForTesting(ctrl),
			)
			require.NoError(t, err)
			testutil.RequireEqualProto(t, &model_starlark_pb.PackageGroup{
				Tree: &model_starlark_pb.PackageGroup_Subpackages{
					IncludeSubpackages: true,
				},
			}, packageGroup.Message)
		})

		t.Run("Duplicate", func(t *testing.T) {
			_, err := model_starlark.NewPackageGroupFromVisibility(
				ctx,
				[]label.ResolvedLabel{
					util.Must(label.NewResolvedLabel("@@foo+//visibility:public")),
					util.Must(label.NewResolvedLabel("@@foo+//visibility:public")),
				},
				encoder,
				&inlinedtree.Options{
					ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
					MaximumSizeBytes: 0,
				},
				NewMockObjectManagerForTesting(ctrl),
			)
			require.EqualError(t, err, "//visibility:public may not be combined with other labels")
		})
	})

	t.Run("Mix", func(t *testing.T) {
		objectManager := NewMockObjectManagerForTesting(ctrl)
		objectManager.EXPECT().CaptureCreatedObject(gomock.Any(), gomock.Any()).AnyTimes()

		packageGroup, err := model_starlark.NewPackageGroupFromVisibility(
			ctx,
			[]label.ResolvedLabel{
				// Inclusion of the root package.
				util.Must(label.NewResolvedLabel("@@toplevel1+//:__pkg__")),
				util.Must(label.NewResolvedLabel("@@toplevel2+//:__subpackages__")),
				// If directories are nested, they should both
				// be represented in the resulting message.
				util.Must(label.NewResolvedLabel("@@nested+//foo:__pkg__")),
				util.Must(label.NewResolvedLabel("@@nested+//foo/bar:__pkg__")),
				// If a parent directory uses __subpackages__,
				// there is no need to preserve any children.
				util.Must(label.NewResolvedLabel("@@collapse1+//foo:__subpackages__")),
				util.Must(label.NewResolvedLabel("@@collapse1+//foo/bar:__pkg__")),
				util.Must(label.NewResolvedLabel("@@collapse1+//foo/bar/baz:__pkg__")),
				// If the children are created before the
				// parent, the children should be removed from
				// the resulting tree.
				util.Must(label.NewResolvedLabel("@@collapse2+//foo/bar/baz:__pkg__")),
				util.Must(label.NewResolvedLabel("@@collapse2+//foo/bar:__pkg__")),
				util.Must(label.NewResolvedLabel("@@collapse2+//foo:__subpackages__")),
				// If "foo" is not provided, it should still be
				// created, because "bar" should go inside it.
				util.Must(label.NewResolvedLabel("@@skip+//foo/bar:__pkg__")),
				// As the order of package groups is irrelevant,
				// they should be deduplicated and sorted.
				util.Must(label.NewResolvedLabel("@@packagegroups+//:group1")),
				util.Must(label.NewResolvedLabel("@@packagegroups+//:group3")),
				util.Must(label.NewResolvedLabel("@@packagegroups+//:group2")),
				util.Must(label.NewResolvedLabel("@@packagegroups+//:group4")),
				util.Must(label.NewResolvedLabel("@@packagegroups+//:group4")),
			},
			encoder,
			&inlinedtree.Options{
				ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				MaximumSizeBytes: 1 << 20,
			},
			objectManager,
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark_pb.PackageGroup{
			Tree: &model_starlark_pb.PackageGroup_Subpackages{
				Overrides: &model_starlark_pb.PackageGroup_Subpackages_OverridesInline{
					OverridesInline: &model_starlark_pb.PackageGroup_Subpackages_Overrides{
						Packages: []*model_starlark_pb.PackageGroup_Package{
							{
								Component: "collapse1+",
								Subpackages: &model_starlark_pb.PackageGroup_Subpackages{
									Overrides: &model_starlark_pb.PackageGroup_Subpackages_OverridesInline{
										OverridesInline: &model_starlark_pb.PackageGroup_Subpackages_Overrides{
											Packages: []*model_starlark_pb.PackageGroup_Package{
												{
													Component:      "foo",
													IncludePackage: true,
													Subpackages: &model_starlark_pb.PackageGroup_Subpackages{
														IncludeSubpackages: true,
													},
												},
											},
										},
									},
								},
							},
							{
								Component: "collapse2+",
								Subpackages: &model_starlark_pb.PackageGroup_Subpackages{
									Overrides: &model_starlark_pb.PackageGroup_Subpackages_OverridesInline{
										OverridesInline: &model_starlark_pb.PackageGroup_Subpackages_Overrides{
											Packages: []*model_starlark_pb.PackageGroup_Package{
												{
													Component:      "foo",
													IncludePackage: true,
													Subpackages: &model_starlark_pb.PackageGroup_Subpackages{
														IncludeSubpackages: true,
													},
												},
											},
										},
									},
								},
							},
							{
								Component: "nested+",
								Subpackages: &model_starlark_pb.PackageGroup_Subpackages{
									Overrides: &model_starlark_pb.PackageGroup_Subpackages_OverridesInline{
										OverridesInline: &model_starlark_pb.PackageGroup_Subpackages_Overrides{
											Packages: []*model_starlark_pb.PackageGroup_Package{
												{
													Component:      "foo",
													IncludePackage: true,
													Subpackages: &model_starlark_pb.PackageGroup_Subpackages{
														Overrides: &model_starlark_pb.PackageGroup_Subpackages_OverridesInline{
															OverridesInline: &model_starlark_pb.PackageGroup_Subpackages_Overrides{
																Packages: []*model_starlark_pb.PackageGroup_Package{
																	{
																		Component:      "bar",
																		IncludePackage: true,
																		Subpackages:    &model_starlark_pb.PackageGroup_Subpackages{},
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
							{
								Component: "skip+",
								Subpackages: &model_starlark_pb.PackageGroup_Subpackages{
									Overrides: &model_starlark_pb.PackageGroup_Subpackages_OverridesInline{
										OverridesInline: &model_starlark_pb.PackageGroup_Subpackages_Overrides{
											Packages: []*model_starlark_pb.PackageGroup_Package{
												{
													Component: "foo",
													Subpackages: &model_starlark_pb.PackageGroup_Subpackages{
														Overrides: &model_starlark_pb.PackageGroup_Subpackages_OverridesInline{
															OverridesInline: &model_starlark_pb.PackageGroup_Subpackages_Overrides{
																Packages: []*model_starlark_pb.PackageGroup_Package{
																	{
																		Component:      "bar",
																		IncludePackage: true,
																		Subpackages:    &model_starlark_pb.PackageGroup_Subpackages{},
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
							{
								Component:      "toplevel1+",
								IncludePackage: true,
								Subpackages:    &model_starlark_pb.PackageGroup_Subpackages{},
							},
							{
								Component:      "toplevel2+",
								IncludePackage: true,
								Subpackages: &model_starlark_pb.PackageGroup_Subpackages{
									IncludeSubpackages: true,
								},
							},
						},
					},
				},
			},
			IncludePackageGroups: []string{
				"@@packagegroups+//:group1",
				"@@packagegroups+//:group2",
				"@@packagegroups+//:group3",
				"@@packagegroups+//:group4",
			},
		}, packageGroup.Message)
	})
}
