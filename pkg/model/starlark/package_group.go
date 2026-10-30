package starlark

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"
)

// packageGroupNode is a simplified representation of the
// PackageGroup.Package and PackageGroup.Subpackages messages. It is
// used to construct new trees contained in PackageGroup.
type packageGroupNode struct {
	includePackage     bool
	includeSubpackages bool
	subpackages        map[string]*packageGroupNode
}

func (n *packageGroupNode) getOrCreate(name string) *packageGroupNode {
	if n.includeSubpackages {
		panic("attempted to look up node under node that already includes all children")
	}
	nSub, ok := n.subpackages[name]
	if !ok {
		nSub = &packageGroupNode{
			subpackages: map[string]*packageGroupNode{},
		}
		n.subpackages[name] = nSub
	}
	return nSub
}

// lookupPackage looks up the node that corresponds to a given canonical
// package name.
func (n *packageGroupNode) lookupPackage(canonicalPackage pg_label.CanonicalPackage) *packageGroupNode {
	nWalk := n.getOrCreate(canonicalPackage.GetCanonicalRepo().String())
	packagePath := canonicalPackage.GetPackagePath()
	for {
		if nWalk.includeSubpackages {
			return nil
		}
		if packagePath == "" {
			return nWalk
		}
		if split := strings.IndexByte(packagePath, '/'); split < 0 {
			nWalk = nWalk.getOrCreate(packagePath)
			packagePath = ""
		} else {
			nWalk = nWalk.getOrCreate(packagePath[:split])
			packagePath = packagePath[split+1:]
		}
	}
}

// toProto converts the data contained in a tree of packageGroupNode to
// its Protobuf message counterpart.
func packageGroupNodeToProto[TMetadata model_core.ReferenceMetadata](ctx context.Context, n *packageGroupNode, encoder model_encoding.DeterministicBinaryEncoder, inlinedTreeOptions *inlinedtree.Options, objectCapturer model_core.CreatedObjectCapturer[TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.PackageGroup_Subpackages, TMetadata], error) {
	inlineCandidates := make(inlinedtree.CandidateList[*model_starlark_pb.PackageGroup_Subpackages, TMetadata], 0, 2)
	defer inlineCandidates.Discard()

	// Set the IncludeSubpackages field.
	inlineCandidates = append(inlineCandidates, inlinedtree.AlwaysInline(
		model_core.NewReferenceMessagePatcher[TMetadata](),
		func(subpackages model_core.PatchedMessage[*model_starlark_pb.PackageGroup_Subpackages, TMetadata]) {
			subpackages.Message.IncludeSubpackages = n.includeSubpackages
		},
	))

	// If one or more subpackages are present, set the overrides field.
	if len(n.subpackages) > 0 {
		overrides := model_starlark_pb.PackageGroup_Subpackages_Overrides{
			Packages: make([]*model_starlark_pb.PackageGroup_Package, 0, len(n.subpackages)),
		}
		patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
		for _, component := range slices.Sorted(maps.Keys(n.subpackages)) {
			nChild := n.subpackages[component]
			subpackages, err := packageGroupNodeToProto[TMetadata](ctx, nChild, encoder, inlinedTreeOptions, objectCapturer)
			if err != nil {
				return model_core.PatchedMessage[*model_starlark_pb.PackageGroup_Subpackages, TMetadata]{}, err
			}
			overrides.Packages = append(overrides.Packages, &model_starlark_pb.PackageGroup_Package{
				Component:      component,
				IncludePackage: nChild.includePackage,
				Subpackages:    subpackages.Message,
			})
			patcher.Merge(subpackages.Patcher)
		}

		inlineCandidates = append(inlineCandidates, inlinedtree.Candidate[*model_starlark_pb.PackageGroup_Subpackages, TMetadata]{
			ExternalMessage: model_core.NewPatchedMessage(model_core.NewProtoBinaryMarshaler(&overrides), patcher),
			Encoder:         encoder,
			ParentAppender: inlinedtree.Capturing(ctx, objectCapturer, func(
				subpackages model_core.PatchedMessage[*model_starlark_pb.PackageGroup_Subpackages, TMetadata],
				externalObject *model_core.Decodable[model_core.MetadataEntry[TMetadata]],
			) {
				if externalObject == nil {
					subpackages.Message.Overrides = &model_starlark_pb.PackageGroup_Subpackages_OverridesInline{
						OverridesInline: &overrides,
					}
				} else {
					subpackages.Message.Overrides = &model_starlark_pb.PackageGroup_Subpackages_OverridesExternal{
						OverridesExternal: subpackages.Patcher.AddDecodableReference(*externalObject),
					}
				}
			}),
		})
	}

	return inlinedtree.Build(inlineCandidates, inlinedTreeOptions)
}

// NewPackageGroupFromVisibility generates a PackageGroup message based
// on a sequence of "visibility" labels provided to repo(), package(),
// or rule targets.
func NewPackageGroupFromVisibility[TMetadata model_core.ReferenceMetadata](ctx context.Context, visibility []pg_label.ResolvedLabel, encoder model_encoding.DeterministicBinaryEncoder, inlinedTreeOptions *inlinedtree.Options, objectCapturer model_core.CreatedObjectCapturer[TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata], error) {
	tree := packageGroupNode{
		subpackages: map[string]*packageGroupNode{},
	}
	var includePackageGroups []string

	for _, resolvedLabel := range visibility {
		canonicalLabel, err := resolvedLabel.AsCanonical()
		if err != nil {
			// Label points to an invalid repo. For
			// consistency with Bazel, discard the entry.
			//
			// TODO: Maybe we should record that this
			// happened as part of the resulting
			// PackageGroup? That way we can improve
			// visibility related error messages to state
			// that this may be caused by invalid labels.
			continue
		}

		canonicalPackage := canonicalLabel.GetCanonicalPackage()
		packagePath := canonicalPackage.GetPackagePath()
		targetName := canonicalLabel.GetTargetName().String()
		if packagePath == "visibility" {
			// Special labels under //visibility:*.
			switch targetName {
			case "private":
				if len(visibility) > 1 {
					return model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata]{}, errors.New("//visibility:private may not be combined with other labels")
				}
				return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.PackageGroup{
					Tree: &model_starlark_pb.PackageGroup_Subpackages{},
				}), nil
			case "public":
				if len(visibility) > 1 {
					return model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata]{}, errors.New("//visibility:public may not be combined with other labels")
				}
				return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.PackageGroup{
					Tree: &model_starlark_pb.PackageGroup_Subpackages{
						IncludeSubpackages: true,
					},
				}), nil
			}
		}

		switch targetName {
		case "__pkg__":
			// Include a single package.
			if n := tree.lookupPackage(canonicalPackage); n != nil {
				n.includePackage = true
			}
		case "__subpackages__":
			// Include a package and all of its children.
			if n := tree.lookupPackage(canonicalPackage); n != nil {
				*n = packageGroupNode{
					includePackage:     true,
					includeSubpackages: true,
				}
			}
		default:
			// Reference to another package group that
			// should be merged into this set of packages.
			includePackageGroups = append(includePackageGroups, canonicalLabel.String())
		}
	}

	treeProto, err := packageGroupNodeToProto[TMetadata](ctx, &tree, encoder, inlinedTreeOptions, objectCapturer)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata]{}, err
	}

	sort.Strings(includePackageGroups)
	return model_core.NewPatchedMessage(
		&model_starlark_pb.PackageGroup{
			Tree:                 treeProto.Message,
			IncludePackageGroups: slices.Compact(includePackageGroups),
		},
		treeProto.Patcher,
	), nil
}

// decodePackageGroupNode converts a tree of packages stored in a
// PackageGroup message back to a tree of packageGroupNode, so that it
// can be modified.
func decodePackageGroupNode[TReference any](
	ctx context.Context,
	overridesReader model_parser.MessageObjectReader[TReference, *model_starlark_pb.PackageGroup_Subpackages_Overrides],
	subpackages model_core.Message[*model_starlark_pb.PackageGroup_Subpackages, TReference],
	includePackage bool,
) (*packageGroupNode, error) {
	n := &packageGroupNode{
		includePackage:     includePackage,
		includeSubpackages: subpackages.Message.GetIncludeSubpackages(),
		subpackages:        map[string]*packageGroupNode{},
	}

	var overrides model_core.Message[*model_starlark_pb.PackageGroup_Subpackages_Overrides, TReference]
	switch o := subpackages.Message.GetOverrides().(type) {
	case *model_starlark_pb.PackageGroup_Subpackages_OverridesInline:
		overrides = model_core.Nested(subpackages, o.OverridesInline)
	case *model_starlark_pb.PackageGroup_Subpackages_OverridesExternal:
		var err error
		overrides, err = model_parser.Dereference(ctx, overridesReader, model_core.Nested(subpackages, o.OverridesExternal))
		if err != nil {
			return nil, fmt.Errorf("failed to load overrides: %w", err)
		}
	case nil:
		return n, nil
	default:
		return nil, errors.New("invalid overrides type")
	}

	for _, p := range overrides.Message.Packages {
		child, err := decodePackageGroupNode(ctx, overridesReader, model_core.Nested(overrides, p.Subpackages), p.IncludePackage)
		if err != nil {
			return nil, fmt.Errorf("package %#v: %w", p.Component, err)
		}
		n.subpackages[p.Component] = child
	}
	return n, nil
}

// AddPackageToPackageGroup returns a copy of a PackageGroup message to
// which a single package has been added. If the package is already
// part of the package group, the original message is returned.
func AddPackageToPackageGroup[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	overridesReader model_parser.MessageObjectReader[TReference, *model_starlark_pb.PackageGroup_Subpackages_Overrides],
	packageGroup model_core.Message[*model_starlark_pb.PackageGroup, TReference],
	canonicalPackage pg_label.CanonicalPackage,
	encoder model_encoding.DeterministicBinaryEncoder,
	inlinedTreeOptions *inlinedtree.Options,
	objectManager model_core.ObjectManager[TReference, TMetadata],
) (model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata], error) {
	tree, err := decodePackageGroupNode(ctx, overridesReader, model_core.Nested(packageGroup, packageGroup.Message.GetTree()), false)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata]{}, err
	}
	if tree.includeSubpackages {
		// Package group is public.
		return model_core.Patch(objectManager, packageGroup), nil
	}
	n := tree.lookupPackage(canonicalPackage)
	if n == nil || n.includePackage {
		// Package is already part of the package group.
		return model_core.Patch(objectManager, packageGroup), nil
	}
	n.includePackage = true

	treeProto, err := packageGroupNodeToProto[TMetadata](ctx, tree, encoder, inlinedTreeOptions, objectManager)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.PackageGroup{
			Tree:                 treeProto.Message,
			IncludePackageGroups: packageGroup.Message.GetIncludePackageGroups(),
		},
		treeProto.Patcher,
	), nil
}

// ObjectManagerForTesting is used to generate mocks for
// NewPackageGroupFromVisibility()'s unit tests.
type ObjectManagerForTesting = model_core.ObjectManager[object.LocalReference, model_core.CreatedObjectTree]
