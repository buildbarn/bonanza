package starlark_test

import (
	"context"
	"errors"
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_core_pb "bonanza.build/pkg/proto/model/core"
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

	encoder := NewMockDeterministicBinaryEncoder(ctrl)
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

// fakeOverridesReader is a reader of PackageGroup.Subpackages.Overrides
// objects that always returns the same object.
type fakeOverridesReader struct {
	overrides *model_starlark_pb.PackageGroup_Subpackages_Overrides
	err       error
}

func (r *fakeOverridesReader) ReadObject(ctx context.Context, reference model_core.Decodable[object.LocalReference]) (model_core.Message[*model_starlark_pb.PackageGroup_Subpackages_Overrides, object.LocalReference], error) {
	if r.err != nil {
		return model_core.Message[*model_starlark_pb.PackageGroup_Subpackages_Overrides, object.LocalReference]{}, r.err
	}
	return model_core.NewSimpleMessage[object.LocalReference](r.overrides), nil
}

func (fakeOverridesReader) GetDecodingParametersSizeBytes() int {
	return 0
}

func TestAddPackageToPackageGroup(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)

	encoder := NewMockDeterministicBinaryEncoder(ctrl)
	encoder.EXPECT().GetDecodingParametersSizeBytes().Return(0).AnyTimes()
	inlinedTreeOptions := &inlinedtree.Options{
		ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
		MaximumSizeBytes: 1 << 20,
	}
	newPackageGroup := func(labels ...string) *model_starlark_pb.PackageGroup {
		resolvedLabels := make([]label.ResolvedLabel, 0, len(labels))
		for _, l := range labels {
			resolvedLabels = append(resolvedLabels, util.Must(label.NewResolvedLabel(l)))
		}
		packageGroup, err := model_starlark.NewPackageGroupFromVisibility(ctx, resolvedLabels, encoder, inlinedTreeOptions, NewMockObjectManagerForTesting(ctrl))
		require.NoError(t, err)
		return packageGroup.Message
	}

	t.Run("Inline", func(t *testing.T) {
		for name, testCase := range map[string]struct {
			labels           []string
			canonicalPackage string
			expectedLabels   []string
		}{
			"Added": {
				labels:           []string{"@@a+//x:__pkg__", "@@a+//:group"},
				canonicalPackage: "@@a+//y",
				expectedLabels:   []string{"@@a+//x:__pkg__", "@@a+//y:__pkg__", "@@a+//:group"},
			},
			"AddedToEmpty": {
				labels:           []string{"@@a+//visibility:private"},
				canonicalPackage: "@@a+//x/y",
				expectedLabels:   []string{"@@a+//x/y:__pkg__"},
			},
			"AddedToParent": {
				labels:           []string{"@@a+//x/y:__pkg__"},
				canonicalPackage: "@@a+//x",
				expectedLabels:   []string{"@@a+//x:__pkg__", "@@a+//x/y:__pkg__"},
			},
			"AlreadyIncluded": {
				labels:           []string{"@@a+//x:__pkg__"},
				canonicalPackage: "@@a+//x",
				expectedLabels:   []string{"@@a+//x:__pkg__"},
			},
			"CoveredBySubpackages": {
				labels:           []string{"@@a+//x:__subpackages__"},
				canonicalPackage: "@@a+//x/y",
				expectedLabels:   []string{"@@a+//x:__subpackages__"},
			},
			"Public": {
				labels:           []string{"@@a+//visibility:public"},
				canonicalPackage: "@@a+//x",
				expectedLabels:   []string{"@@a+//visibility:public"},
			},
		} {
			t.Run(name, func(t *testing.T) {
				packageGroup, err := model_starlark.AddPackageToPackageGroup(
					ctx,
					/* overridesReader = */ nil,
					model_core.NewSimpleMessage[object.LocalReference](newPackageGroup(testCase.labels...)),
					util.Must(label.NewCanonicalPackage(testCase.canonicalPackage)),
					encoder,
					inlinedTreeOptions,
					NewMockObjectManagerForTesting(ctrl),
				)
				require.NoError(t, err)
				testutil.RequireEqualProto(t, newPackageGroup(testCase.expectedLabels...), packageGroup.Message)
			})
		}
	})

	t.Run("ExternalOverrides", func(t *testing.T) {
		// Overrides that are stored in separate objects should
		// be loaded, so that the package can be added to them.
		packageGroup := model_core.NewMessage(
			&model_starlark_pb.PackageGroup{
				Tree: &model_starlark_pb.PackageGroup_Subpackages{
					Overrides: &model_starlark_pb.PackageGroup_Subpackages_OverridesExternal{
						OverridesExternal: &model_core_pb.DecodableReference{
							Reference: &model_core_pb.Reference{Index: 1},
						},
					},
				},
				IncludePackageGroups: []string{"@@a+//:group"},
			},
			object.OutgoingReferencesList[object.LocalReference]{
				object.MustNewSHA256V1LocalReference("629db2c2c2a8ae9445ffed22cbf2f3b5a89d7062a7eb4cbdf369217b10e99838", 1337, 0, 0, 0),
			},
		)
		overrides := newPackageGroup("@@a+//x:__pkg__").Tree.GetOverridesInline()
		require.NotNil(t, overrides)

		t.Run("Success", func(t *testing.T) {
			result, err := model_starlark.AddPackageToPackageGroup(
				ctx,
				&fakeOverridesReader{overrides: overrides},
				packageGroup,
				util.Must(label.NewCanonicalPackage("@@a+//y")),
				encoder,
				inlinedTreeOptions,
				NewMockObjectManagerForTesting(ctrl),
			)
			require.NoError(t, err)
			testutil.RequireEqualProto(t, newPackageGroup("@@a+//x:__pkg__", "@@a+//y:__pkg__", "@@a+//:group"), result.Message)
		})

		t.Run("ReadFailure", func(t *testing.T) {
			_, err := model_starlark.AddPackageToPackageGroup(
				ctx,
				&fakeOverridesReader{err: errors.New("server offline")},
				packageGroup,
				util.Must(label.NewCanonicalPackage("@@a+//y")),
				encoder,
				inlinedTreeOptions,
				NewMockObjectManagerForTesting(ctrl),
			)
			require.EqualError(t, err, "failed to load overrides: server offline")
		})
	})
}
