package analysis

import (
	"testing"

	"bonanza.build/pkg/glob"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestLabelAttrRequirementsAllowFiles(t *testing.T) {
	newRequirements := func(t *testing.T, allowFiles *model_starlark_pb.Attr_AllowFiles, allowSingleFile bool) *labelAttrRequirements[object.LocalReference, model_core.CreatedObjectTree] {
		labelOptions := &model_starlark_pb.Attr_LabelOptions{
			AllowFiles: allowFiles,
			Providers:  []*model_starlark_pb.ProviderSet{{}},
		}
		attr := &model_starlark_pb.Attr{
			Type: &model_starlark_pb.Attr_LabelList{
				LabelList: &model_starlark_pb.Attr_LabelListType{
					ListValueOptions: labelOptions,
				},
			},
		}
		if allowSingleFile {
			attr.Type = &model_starlark_pb.Attr_Label{
				Label: &model_starlark_pb.Attr_LabelType{
					ValueOptions:    labelOptions,
					AllowSingleFile: true,
				},
			}
		}
		valueReaders := &model_starlark.ValueReaders[object.LocalReference]{}
		thread := eval.NewThread(t.Context(), eval.Universe())
		thread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{
			Readers: valueReaders,
		})
		requirements, err := newLabelAttrRequirements[object.LocalReference, model_core.CreatedObjectTree](
			t.Context(),
			thread,
			valueReaders,
			attr,
		)
		require.NoError(t, err)
		return requirements
	}
	check := func(r *labelAttrRequirements[object.LocalReference, model_core.CreatedObjectTree], targetLabel string, isFileTarget bool, providerInstances []*model_starlark_pb.Struct) error {
		return r.check(targetLabel, model_core.NewSimpleMessage[object.LocalReference](&model_analysis_pb.TargetProviders_Value{
			ProviderInstances: providerInstances,
			IsFileTarget:      isFileTarget,
		}))
	}

	// Provider instances of a target whose DefaultInfo.files
	// contains the provided files.
	newProviderInstances := func(files ...*model_starlark_pb.File) []*model_starlark_pb.Struct {
		elements := make([]*model_starlark_pb.List_Element, 0, len(files))
		for _, file := range files {
			elements = append(elements, &model_starlark_pb.List_Element{
				Level: &model_starlark_pb.List_Element_Leaf{
					Leaf: &model_starlark_pb.Value{
						Kind: &model_starlark_pb.Value_File{File: file},
					},
				},
			})
		}
		return []*model_starlark_pb.Struct{{
			Fields: &model_starlark_pb.Struct_Fields{
				Keys: []string{"files"},
				Values: []*model_starlark_pb.List_Element{{
					Level: &model_starlark_pb.List_Element_Leaf{
						Leaf: &model_starlark_pb.Value{
							Kind: &model_starlark_pb.Value_Depset{
								Depset: &model_starlark_pb.Depset{Elements: elements},
							},
						},
					},
				}},
			},
			ProviderInstanceProperties: &model_starlark_pb.Provider_InstanceProperties{
				ProviderIdentifier: defaultInfoProviderIdentifier.String(),
			},
		}}
	}
	newOutputFile := func(fileLabel string, fileType model_starlark_pb.File_Owner_Type) *model_starlark_pb.File {
		return &model_starlark_pb.File{
			Label: fileLabel,
			Owner: &model_starlark_pb.File_Owner{
				TargetName: "rule",
				Type:       fileType,
			},
		}
	}

	// Provider instances of a file target, whose DefaultInfo.files
	// only contains the file itself.
	newSourceFileProviderInstances := func(fileLabel string) []*model_starlark_pb.Struct {
		return newProviderInstances(&model_starlark_pb.File{Label: fileLabel})
	}
	newSuffixes := func(suffixes ...string) *model_starlark_pb.Attr_AllowFiles {
		return &model_starlark_pb.Attr_AllowFiles{
			FilenamePattern: util.Must(glob.NewNFAFromSuffixes(suffixes)).Bytes(),
		}
	}

	t.Run("NoFiles", func(t *testing.T) {
		// If allow_files is not set, file targets are not
		// permitted. Rule targets are permitted, regardless of
		// the files they yield.
		r := newRequirements(t, nil, false)
		require.EqualError(
			t,
			check(r, "@@main+//:foo.txt", true, newSourceFileProviderInstances("@@main+//:foo.txt")),
			"target \"@@main+//:foo.txt\" is a file target, which is not permitted as allow_files is not set",
		)
		require.NoError(t, check(r, "@@main+//:rule", false, nil))
		require.NoError(t, check(r, "@@main+//:rule", false, newProviderInstances(
			newOutputFile("@@main+//:a.cc", model_starlark_pb.File_Owner_FILE),
		)))
	})

	t.Run("AllFiles", func(t *testing.T) {
		// If allow_files=True, all targets are permitted,
		// regardless of the files they yield.
		r := newRequirements(t, &model_starlark_pb.Attr_AllowFiles{}, false)
		require.NoError(t, check(r, "@@main+//:foo.txt", true, newSourceFileProviderInstances("@@main+//:foo.txt")))
		require.NoError(t, check(r, "@@main+//:rule", false, nil))
	})

	t.Run("Suffixes", func(t *testing.T) {
		r := newRequirements(t, newSuffixes(".txt"), false)

		// As DefaultInfo.files of file targets only contains
		// the file itself, file targets are matched by name.
		require.NoError(t, check(r, "@@main+//:foo.txt", true, newSourceFileProviderInstances("@@main+//:foo.txt")))
		require.NoError(t, check(r, "@@main+//:dir/foo.txt", true, newSourceFileProviderInstances("@@main+//:dir/foo.txt")))
		require.EqualError(
			t,
			check(r, "@@main+//:foo.cc", true, newSourceFileProviderInstances("@@main+//:foo.cc")),
			"target \"@@main+//:foo.cc\" does not produce any files permitted by allow_files",
		)

		// Rule targets need to yield at least one file with a
		// matching name, or a directory. Files that don't
		// match are permitted.
		require.NoError(t, check(r, "@@main+//:rule", false, newProviderInstances(
			newOutputFile("@@main+//:a.cc", model_starlark_pb.File_Owner_FILE),
			newOutputFile("@@main+//:b.txt", model_starlark_pb.File_Owner_FILE),
		)))
		require.NoError(t, check(r, "@@main+//:rule", false, newProviderInstances(
			newOutputFile("@@main+//:dir", model_starlark_pb.File_Owner_DIRECTORY),
		)))
		require.EqualError(
			t,
			check(r, "@@main+//:rule", false, newProviderInstances(
				newOutputFile("@@main+//:a.cc", model_starlark_pb.File_Owner_FILE),
			)),
			"target \"@@main+//:rule\" does not produce any files permitted by allow_files",
		)
		require.EqualError(
			t,
			check(r, "@@main+//:rule", false, newProviderInstances()),
			"target \"@@main+//:rule\" does not produce any files permitted by allow_files",
		)
		require.EqualError(
			t,
			check(r, "@@main+//:rule", false, nil),
			"target with label \"@@main+//:rule\" did not yield provider \"@@builtins_core+//:exports.bzl%DefaultInfo\"",
		)
	})

	t.Run("SingleFile", func(t *testing.T) {
		// allow_single_file=True requires targets to yield
		// exactly one file.
		r := newRequirements(t, &model_starlark_pb.Attr_AllowFiles{}, true)
		require.NoError(t, check(r, "@@main+//:foo.cc", true, newSourceFileProviderInstances("@@main+//:foo.cc")))
		require.NoError(t, check(r, "@@main+//:rule", false, newProviderInstances(
			newOutputFile("@@main+//:a.cc", model_starlark_pb.File_Owner_FILE),
		)))
		require.EqualError(
			t,
			check(r, "@@main+//:rule", false, newProviderInstances(
				newOutputFile("@@main+//:a.cc", model_starlark_pb.File_Owner_FILE),
				newOutputFile("@@main+//:b.cc", model_starlark_pb.File_Owner_FILE),
			)),
			"target \"@@main+//:rule\" must produce a single file, but it produces 2 files",
		)
	})

	t.Run("SingleFileWithSuffixes", func(t *testing.T) {
		r := newRequirements(t, newSuffixes(".cc"), true)
		require.NoError(t, check(r, "@@main+//:rule", false, newProviderInstances(
			newOutputFile("@@main+//:a.cc", model_starlark_pb.File_Owner_FILE),
		)))
		require.EqualError(
			t,
			check(r, "@@main+//:rule", false, newProviderInstances(
				newOutputFile("@@main+//:a.h", model_starlark_pb.File_Owner_FILE),
			)),
			"target \"@@main+//:rule\" does not produce any files permitted by allow_files",
		)
	})

	t.Run("FileTargetsSkipProviders", func(t *testing.T) {
		// Like Bazel, file targets are not checked against
		// the required providers.
		r := newRequirements(t, &model_starlark_pb.Attr_AllowFiles{}, false)
		r.requiredProviders = []*model_starlark_pb.ProviderSet{{
			ProviderIdentifiers: []string{"@@rules_cc+//cc:defs.bzl%CcInfo"},
		}}
		require.NoError(t, check(r, "@@main+//:foo.cc", true, newSourceFileProviderInstances("@@main+//:foo.cc")))
		require.EqualError(
			t,
			check(r, "@@main+//:rule", false, newProviderInstances()),
			"target \"@@main+//:rule\" does not have all mandatory providers of any of the sets [@@rules_cc+//cc:defs.bzl%CcInfo]",
		)
	})
}
