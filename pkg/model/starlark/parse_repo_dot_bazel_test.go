package starlark_test

import (
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
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

func TestParseModuleDotBazel(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)

	encoder := NewMockDeterministicBinaryEncoder(ctrl)
	encoder.EXPECT().GetDecodingParametersSizeBytes().Return(0).AnyTimes()

	t.Run("Empty", func(t *testing.T) {
		// If no calls to repo() are made, the resulting
		// attributes should be identical to the constant
		// message value we provide.
		labelResolver := NewMockLabelResolver(ctrl)

		defaultAttrs, err := model_starlark.ParseRepoDotBazel[object.LocalReference](
			ctx,
			"",
			util.Must(label.NewCanonicalLabel("@@foo+//:REPO.bazel")),
			encoder,
			&inlinedtree.Options{
				ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				MaximumSizeBytes: 0,
			},
			model_core.ObjectManager[object.LocalReference, model_core.ReferenceMetadata](nil),
			labelResolver,
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark.DefaultInheritableAttrs, defaultAttrs.Message)
	})

	t.Run("NoArguments", func(t *testing.T) {
		// It should be valid to call repo() without any
		// arguments. In that case the returned attributes
		// should also be equal to the default.
		labelResolver := NewMockLabelResolver(ctrl)

		defaultAttrs, err := model_starlark.ParseRepoDotBazel[object.LocalReference](
			ctx,
			"repo()",
			util.Must(label.NewCanonicalLabel("@@foo+//:REPO.bazel")),
			encoder,
			&inlinedtree.Options{
				ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				MaximumSizeBytes: 0,
			},
			model_core.ObjectManager[object.LocalReference, model_core.ReferenceMetadata](nil),
			labelResolver,
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark.DefaultInheritableAttrs, defaultAttrs.Message)
	})

	t.Run("RedundantCalls", func(t *testing.T) {
		// Calling repo() times is not permitted.
		labelResolver := NewMockLabelResolver(ctrl)

		_, err := model_starlark.ParseRepoDotBazel[object.LocalReference](
			ctx,
			"repo()\nrepo()",
			util.Must(label.NewCanonicalLabel("@@foo+//:REPO.bazel")),
			encoder,
			&inlinedtree.Options{
				ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				MaximumSizeBytes: 0,
			},
			model_core.ObjectManager[object.LocalReference, model_core.ReferenceMetadata](nil),
			labelResolver,
		)
		require.EqualError(t, err, "repo: function can only be invoked once")
	})

	t.Run("ApplicableLicensesAndPackageMetadata", func(t *testing.T) {
		// default_applicable_licenses is an alias of
		// default_package_metadata. It's not possible to
		// provide both arguments at once.
		labelResolver := NewMockLabelResolver(ctrl)

		_, err := model_starlark.ParseRepoDotBazel[object.LocalReference](
			ctx,
			`repo(
				default_applicable_licenses = ["//:license"],
				default_package_metadata = ["//:metadata"],
			)`,
			util.Must(label.NewCanonicalLabel("@@foo+//:REPO.bazel")),
			encoder,
			&inlinedtree.Options{
				ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				MaximumSizeBytes: 0,
			},
			model_core.ObjectManager[object.LocalReference, model_core.ReferenceMetadata](nil),
			labelResolver,
		)
		require.EqualError(t, err, "repo: default_applicable_licenses and default_package_metadata are mutually exclusive")
	})

	t.Run("AllArguments", func(t *testing.T) {
		// Example invocation where all supported arguments are
		// provided.
		objectManager := NewMockObjectManagerForTesting(ctrl)
		objectManager.EXPECT().CaptureCreatedObject(gomock.Any(), gomock.Any()).AnyTimes()
		labelResolver := NewMockLabelResolver(ctrl)

		defaultAttrs, err := model_starlark.ParseRepoDotBazel[object.LocalReference](
			ctx,
			`repo(
				default_deprecation = "All code in this repository is deprecated.",
				default_package_metadata = ["//:metadata"],
				default_testonly = True,
				default_visibility = [
					"//somepackage:__pkg__",
				],
			)`,
			util.Must(label.NewCanonicalLabel("@@foo+//:REPO.bazel")),
			encoder,
			&inlinedtree.Options{
				ReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				MaximumSizeBytes: 0,
			},
			objectManager,
			labelResolver,
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark_pb.InheritableAttrs{
			Deprecation: "All code in this repository is deprecated.",
			PackageMetadata: []string{
				"@@foo+//:metadata",
			},
			Testonly: true,
			Visibility: &model_starlark_pb.PackageGroup{
				Tree: &model_starlark_pb.PackageGroup_Subpackages{
					Overrides: &model_starlark_pb.PackageGroup_Subpackages_OverridesInline{
						OverridesInline: &model_starlark_pb.PackageGroup_Subpackages_Overrides{
							Packages: []*model_starlark_pb.PackageGroup_Package{{
								Component: "foo+",
								Subpackages: &model_starlark_pb.PackageGroup_Subpackages{
									Overrides: &model_starlark_pb.PackageGroup_Subpackages_OverridesInline{
										OverridesInline: &model_starlark_pb.PackageGroup_Subpackages_Overrides{
											Packages: []*model_starlark_pb.PackageGroup_Package{
												{
													Component:      "somepackage",
													IncludePackage: true,
													Subpackages:    &model_starlark_pb.PackageGroup_Subpackages{},
												},
											},
										},
									},
								},
							}},
						},
					},
				},
			},
		}, defaultAttrs.Message)
	})
}
