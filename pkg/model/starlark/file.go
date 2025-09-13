package starlark

import (
	"errors"
	"fmt"
	"hash/fnv"
	go_path "path"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	bb_path "github.com/buildbarn/bb-storage/pkg/filesystem/path"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Names of commonly used pathname components of source and output files.
const (
	ComponentStrBazelOut = "bazel-out"
	ComponentStrBin      = "bin"
	ComponentStrExternal = "external"
)

// Typed instances of the names specified above.
var (
	ComponentBazelOut = bb_path.MustNewComponent(ComponentStrBazelOut)
	ComponentBin      = bb_path.MustNewComponent(ComponentStrBin)
	ComponentExternal = bb_path.MustNewComponent(ComponentStrExternal)
)

type File[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	definition       model_core.Message[*model_starlark_pb.File, TReference]
	treeRelativePath *bb_path.Trace
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*File[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ starlark.Comparable                                                 = (*File[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ starlark.HasAttrs                                                   = (*File[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

func NewFile[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](definition model_core.Message[*model_starlark_pb.File, TReference]) *File[TReference, TMetadata] {
	return &File[TReference, TMetadata]{
		definition: definition,
	}
}

// WithTreeRelativePath can be used by DirectoryExpander.expand() to
// convert a File of a directory to an instance that refers to a regular
// file contained within the directory.
func (f *File[TReference, TMetadata]) WithTreeRelativePath(treeRelativePath *bb_path.Trace) *File[TReference, TMetadata] {
	return &File[TReference, TMetadata]{
		definition:       f.definition,
		treeRelativePath: treeRelativePath,
	}
}

func (f *File[TReference, TMetadata]) String() string {
	if p, err := FileGetInputRootPath(f.definition, f.treeRelativePath); err == nil {
		return fmt.Sprintf("<File %s>", p)
	}
	return "<File>"
}

func (File[TReference, TMetadata]) Type() string {
	return "File"
}

func (File[TReference, TMetadata]) Freeze() {
}

func (File[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (f *File[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	d := f.definition.Message
	h := fnv.New32a()
	h.Write([]byte(d.Label))
	return h.Sum32(), nil
}

func (f *File[TReference, TMetadata]) equals(other *File[TReference, TMetadata]) bool {
	return f == other || model_core.MessagesEqual(f.definition, other.definition)
}

func (f *File[TReference, TMetadata]) CompareSameType(thread *starlark.Thread, op syntax.Token, other starlark.Value, depth int) (bool, error) {
	switch op {
	case syntax.EQL:
		return f.equals(other.(*File[TReference, TMetadata])), nil
	case syntax.NEQ:
		return !f.equals(other.(*File[TReference, TMetadata])), nil
	default:
		return false, errors.New("File can only be compared for equality")
	}
}

// ConfigurationReferenceToComponent determines the pathname component
// to use for a given configuration, so that it may be embedded into
// bazel-out/.../bin pathnames.
func ConfigurationReferenceToComponent[TReference object.BasicReference](configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference]) (string, error) {
	if configurationReference.Message == nil {
		// The configuration is empty, meaning all build
		// settings are set to their default values. Use the
		// string "none" to denote this, akin config.none().
		return "none", nil
	}

	// The configuration is non-empty. Put the reference of the
	// configuration and its decoding parameters in the pathname. In
	// addition to guaranteeing there are no collisions, it makes it
	// easy to inspect the configuration that was used to build
	// these files.
	r, err := model_core.FlattenDecodableReference(configurationReference)
	if err != nil {
		return "", err
	}
	return model_core.DecodableLocalReferenceToString(r), nil
}

func (f *File[TReference, TMetadata]) getPathEnd() (string, error) {
	if f.treeRelativePath != nil {
		return f.treeRelativePath.GetUNIXString(), nil
	}
	labelStr := f.definition.Message.Label
	canonicalLabel, err := pg_label.NewCanonicalLabel(labelStr)
	if err != nil {
		return "", fmt.Errorf("invalid canonical label %#v: %w", labelStr, err)
	}
	return canonicalLabel.GetTargetName().String(), nil
}

func (f *File[TReference, TMetadata]) Attr(thread *starlark.Thread, name string) (starlark.Value, error) {
	d := f.definition.Message
	switch name {
	case "basename":
		pathEnd, err := f.getPathEnd()
		if err != nil {
			return nil, fmt.Errorf("invalid canonical label %#v: %w", d.Label, err)
		}
		return starlark.String(go_path.Base(pathEnd)), nil
	case "dirname":
		p, err := FileGetInputRootPath(f.definition, f.treeRelativePath)
		if err != nil {
			return nil, err
		}
		return starlark.String(go_path.Dir(p)), nil
	case "extension":
		p, err := f.getPathEnd()
		if err != nil {
			return nil, fmt.Errorf("invalid canonical label %#v: %w", d.Label, err)
		}
		for i := len(p) - 1; i >= 0 && p[i] != '/' && p[i] != ':'; i-- {
			if p[i] == '.' {
				return starlark.String(p[i+1:]), nil
			}
		}
		return starlark.String(""), nil
	case "label":
		// Bonanza specific extension.
		if f.treeRelativePath != nil {
			return nil, errors.New("files with tree relative paths cannot be identified with a label")
		}
		canonicalLabel, err := pg_label.NewCanonicalLabel(d.Label)
		if err != nil {
			return nil, fmt.Errorf("invalid canonical label %#v: %w", d.Label, err)
		}
		return NewLabel[TReference, TMetadata](canonicalLabel.AsResolved()), nil
	case "is_directory":
		// For files created by DirectoryExpander, the
		// definition still refers to the directory from which
		// the files originated.
		return starlark.Bool(d.Owner != nil && d.Owner.Type == model_starlark_pb.File_Owner_DIRECTORY && f.treeRelativePath == nil), nil
	case "is_source":
		return starlark.Bool(d.Owner == nil), nil
	case "is_symlink":
		return starlark.Bool(d.Owner != nil && d.Owner.Type == model_starlark_pb.File_Owner_SYMLINK), nil
	case "owner":
		canonicalLabel, err := pg_label.NewCanonicalLabel(d.Label)
		if err != nil {
			return nil, fmt.Errorf("invalid canonical label %#v: %w", d.Label, err)
		}

		// If the file is an output file, return the label of
		// the target that generates it. If it is a source file,
		// return a label of the file itself.
		if o := d.Owner; o != nil {
			targetName, err := pg_label.NewTargetName(o.TargetName)
			if err != nil {
				return nil, fmt.Errorf("invalid owner target name %#v: %w", o.TargetName, err)
			}
			canonicalLabel = canonicalLabel.GetCanonicalPackage().AppendTargetName(targetName)
		}

		return NewLabel[TReference, TMetadata](canonicalLabel.AsResolved()), nil
	case "path":
		p, err := FileGetInputRootPath(f.definition, f.treeRelativePath)
		if err != nil {
			return nil, err
		}
		return starlark.String(p), nil
	case "root":
		parts, err := appendFileOwnerToPath(f.definition, make([]string, 0, 6))
		if err != nil {
			return nil, err
		}
		rootPath := ""
		if len(parts) > 0 {
			rootPath = go_path.Join(parts...)
		}
		return newStructFromLists[TReference, TMetadata](
			nil,
			[]string{"path"},
			[]any{starlark.String(rootPath)},
		), nil
	case "short_path":
		canonicalLabel, err := pg_label.NewCanonicalLabel(d.Label)
		if err != nil {
			return nil, fmt.Errorf("invalid canonical label %#v: %w", d.Label, err)
		}
		canonicalPackage := canonicalLabel.GetCanonicalPackage()
		return starlark.String(go_path.Join(
			"..",
			canonicalPackage.GetCanonicalRepo().String(),
			canonicalPackage.GetPackagePath(),
			canonicalLabel.GetTargetName().String(),
			f.treeRelativePath.GetUNIXString(),
		)), nil
	case "tree_relative_path":
		if f.treeRelativePath == nil {
			return nil, errors.New("File.tree_relative_path is only available during Args.add_*() directory expansion ")
		}
		return starlark.String(f.treeRelativePath.GetUNIXString()), nil
	default:
		return nil, nil
	}
}

var fileAttrNames = []string{
	"basename",
	"dirname",
	"extension",
	"label",
	"is_directory",
	"is_source",
	"is_symlink",
	"owner",
	"path",
	"root",
	"short_path",
	"tree_relative_path",
}

func (File[TReference, TMetadata]) AttrNames() []string {
	return fileAttrNames
}

func (f *File[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	if f.treeRelativePath != nil {
		panic("files with tree relative paths should not be encoded, as they only exist during target action command computation")
	}
	d := model_core.Patch(options.ObjectCapturer, f.definition)
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_File{
				File: d.Message,
			},
		},
		d.Patcher,
	), false, nil
}

func (f *File[TReference, TMetadata]) GetDefinition() model_core.Message[*model_starlark_pb.File, TReference] {
	return f.definition
}

func (f *File[TReference, TMetadata]) GetTreeRelativePath() *bb_path.Trace {
	return f.treeRelativePath
}

// FileGetInputRootPath returns the full input root path corresponding
// to a File object, similar to accessing the "path" attribute of a File
// from within Starlark code.
func FileGetInputRootPath[TReference object.BasicReference](f model_core.Message[*model_starlark_pb.File, TReference], treeRelativePath *bb_path.Trace) (string, error) {
	canonicalLabel, err := pg_label.NewCanonicalLabel(f.Message.Label)
	if err != nil {
		return "", fmt.Errorf("invalid canonical label %#v: %w", f.Message.Label, err)
	}
	parts, err := appendFileOwnerToPath(f, make([]string, 0, 7))
	if err != nil {
		return "", err
	}
	canonicalPackage := canonicalLabel.GetCanonicalPackage()
	return go_path.Join(
		append(
			parts,
			ComponentStrExternal,
			canonicalPackage.GetCanonicalRepo().String(),
			canonicalPackage.GetPackagePath(),
			canonicalLabel.GetTargetName().String(),
			treeRelativePath.GetUNIXString(),
		)...,
	), nil
}

// FileGetRunfilesPath returns a runfiles root directory relative path
// corresponding to a File object.
func FileGetRunfilesPath[TReference object.BasicReference](f model_core.Message[*model_starlark_pb.File, TReference]) (string, error) {
	canonicalLabel, err := pg_label.NewCanonicalLabel(f.Message.Label)
	if err != nil {
		return "", fmt.Errorf("invalid canonical label %#v: %w", f.Message.Label, err)
	}
	canonicalPackage := canonicalLabel.GetCanonicalPackage()
	return go_path.Join(
		canonicalPackage.GetCanonicalRepo().String(),
		canonicalPackage.GetPackagePath(),
		canonicalLabel.GetTargetName().String(),
	), nil
}

func appendFileOwnerToPath[TReference object.BasicReference](f model_core.Message[*model_starlark_pb.File, TReference], parts []string) ([]string, error) {
	if o := f.Message.Owner; o != nil {
		configurationComponent, err := ConfigurationReferenceToComponent(model_core.Nested(f, o.ConfigurationReference))
		if err != nil {
			return nil, err
		}
		parts = append(
			parts,
			ComponentStrBazelOut,
			configurationComponent,
			ComponentStrBin,
		)
	}
	return parts, nil
}
