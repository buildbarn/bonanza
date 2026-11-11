package analysis

import (
	"container/heap"
	"context"
	"errors"
	"slices"

	"bonanza.build/pkg/ds"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

// packageTargetsCursor is used by ComputePackageTargetsValue to
// traverse a B-tree of targets, so that it can be merged with other
// B-trees of targets.
type packageTargetsCursor[TReference any] struct {
	// Lists of entries that still need to be processed. The last
	// list contains entries at the deepest level of the B-tree that
	// is currently being traversed.
	lists []model_core.Message[[]*model_analysis_pb.Package_Value_Target, TReference]

	// The name of the first target contained in the entry that is
	// to be processed next.
	firstName string
}

// newPackageTargetsCursor creates a cursor for traversing a non-empty
// B-tree of targets.
func newPackageTargetsCursor[TReference any](targets model_core.Message[[]*model_analysis_pb.Package_Value_Target, TReference]) (*packageTargetsCursor[TReference], error) {
	pc := &packageTargetsCursor[TReference]{
		lists: []model_core.Message[[]*model_analysis_pb.Package_Value_Target, TReference]{targets},
	}
	return pc, pc.updateFirstName()
}

// updateFirstName updates the cached name of the first target in the
// entry that is to be processed next.
func (pc *packageTargetsCursor[TReference]) updateFirstName() error {
	switch level := pc.head().Message.Level.(type) {
	case *model_analysis_pb.Package_Value_Target_Leaf:
		pc.firstName = level.Leaf.Name
	case *model_analysis_pb.Package_Value_Target_Parent_:
		pc.firstName = level.Parent.FirstName
	default:
		return errors.New("invalid target level type")
	}
	return nil
}

// head returns the entry that is to be processed next.
func (pc *packageTargetsCursor[TReference]) head() model_core.Message[*model_analysis_pb.Package_Value_Target, TReference] {
	list := pc.lists[len(pc.lists)-1]
	return model_core.Nested(list, list.Message[0])
}

// advance removes the entry that is to be processed next. If the entry
// is a parent node, its children may be provided, so that they are
// processed instead. It returns false if no entries remain.
func (pc *packageTargetsCursor[TReference]) advance(children model_core.Message[[]*model_analysis_pb.Package_Value_Target, TReference]) (bool, error) {
	list := &pc.lists[len(pc.lists)-1]
	list.Message = list.Message[1:]
	if len(children.Message) > 0 {
		pc.lists = append(pc.lists, children)
	}
	for len(pc.lists) > 0 && len(pc.lists[len(pc.lists)-1].Message) == 0 {
		pc.lists = pc.lists[:len(pc.lists)-1]
	}
	if len(pc.lists) == 0 {
		return false, nil
	}
	return true, pc.updateFirstName()
}

// packageTargetsCursorHeap is a heap of cursors of B-trees of targets,
// ordered by the name of the first target in the entry that is to be
// processed next.
type packageTargetsCursorHeap[TReference any] struct {
	ds.Slice[*packageTargetsCursor[TReference]]
}

func (h packageTargetsCursorHeap[TReference]) Less(i, j int) bool {
	return h.Slice[i].firstName < h.Slice[j].firstName
}

func (c *baseComputer[TReference, TMetadata]) ComputePackageTargetsValue(ctx context.Context, key *model_analysis_pb.PackageTargets_Key, e PackageTargetsEnvironment[TReference, TMetadata]) (PatchedPackageTargetsValue[TMetadata], error) {
	analysisReaders, gotAnalysisReaders := getAnalysisReaders(e)

	packageValue := e.GetPackageValue(&model_analysis_pb.Package_Key{
		Label:             key.Package,
		MacroInstanceName: key.MacroInstanceName,
	})
	if !packageValue.IsSet() || !gotAnalysisReaders {
		return PatchedPackageTargetsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	// Create cursors for the targets declared by the package or
	// macro instance itself, and for all targets declared by the
	// macro instances it declares.
	var cursors packageTargetsCursorHeap[TReference]
	addCursor := func(targets model_core.Message[[]*model_analysis_pb.Package_Value_Target, TReference]) error {
		if len(targets.Message) > 0 {
			cursor, err := newPackageTargetsCursor(targets)
			if err != nil {
				return err
			}
			cursors.Slice = append(cursors.Slice, cursor)
		}
		return nil
	}
	if err := addCursor(model_core.Nested(packageValue, packageValue.Message.Targets)); err != nil {
		return PatchedPackageTargetsValue[TMetadata]{}, err
	}

	// Request the targets of all macro instances before returning
	// ErrMissingDependency, so that they can be expanded in
	// parallel.
	missingDependencies := false
	var errIter error
	for entry := range btree.AllLeaves(
		ctx,
		analysisReaders.MacroExpansionContextEntry,
		model_core.Nested(packageValue, packageValue.Message.MacroExpansionContexts),
		func(entry model_core.Message[*model_starlark_pb.MacroExpansionContext_Entry, TReference]) (*model_core_pb.DecodableReference, error) {
			if level, ok := entry.Message.Level.(*model_starlark_pb.MacroExpansionContext_Entry_Parent_); ok {
				return level.Parent.Reference, nil
			}
			return nil, nil
		},
		&errIter,
	) {
		level, ok := entry.Message.Level.(*model_starlark_pb.MacroExpansionContext_Entry_Leaf)
		if !ok {
			return PatchedPackageTargetsValue[TMetadata]{}, errors.New("not a valid leaf entry")
		}
		if key.ExcludeFinalizers && level.Leaf.Definition.GetRootMacroInstanceDefinition().GetFinalizer() == model_starlark_pb.MacroInstance_FINALIZER {
			continue
		}
		macroInstanceTargets := e.GetPackageTargetsValue(&model_analysis_pb.PackageTargets_Key{
			Package:           key.Package,
			MacroInstanceName: append(slices.Clip(key.MacroInstanceName), level.Leaf.NameSuffix),
			ExcludeFinalizers: key.ExcludeFinalizers,
		})
		if !macroInstanceTargets.IsSet() {
			missingDependencies = true
			continue
		}
		if err := addCursor(model_core.Nested(macroInstanceTargets, macroInstanceTargets.Message.Targets)); err != nil {
			return PatchedPackageTargetsValue[TMetadata]{}, err
		}
	}
	if errIter != nil {
		return PatchedPackageTargetsValue[TMetadata]{}, errIter
	}
	if missingDependencies {
		return PatchedPackageTargetsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	if len(cursors.Slice) == 1 {
		// Only a single list of targets is non-empty, meaning it
		// can be returned as is.
		targets := cursors.Slice[0].lists[0]
		return model_core.Patch(
			e,
			model_core.Nested(targets, &model_analysis_pb.PackageTargets_Value{
				Targets: targets.Message,
			}),
		), nil
	}

	// Merge all lists of targets. Subtrees that don't overlap with
	// any of the other lists are copied without being traversed.
	heap.Init(&cursors)

	treeBuilder := c.newPackageTargetsTreeBuilder(ctx, e)
	defer treeBuilder.Discard()
	for cursors.Len() > 0 {
		cursor := heap.Pop(&cursors).(*packageTargetsCursor[TReference])
		entry := cursor.head()
		var children model_core.Message[[]*model_analysis_pb.Package_Value_Target, TReference]
		switch level := entry.Message.Level.(type) {
		case *model_analysis_pb.Package_Value_Target_Leaf:
			if err := treeBuilder.PushChild(model_core.Patch(e, entry)); err != nil {
				return PatchedPackageTargetsValue[TMetadata]{}, err
			}
		case *model_analysis_pb.Package_Value_Target_Parent_:
			parent := level.Parent
			if cursors.Len() == 0 || parent.LastName < cursors.Slice[0].firstName {
				// None of the other lists contain any
				// targets in the range of this subtree.
				// Copy it in its entirety.
				if err := treeBuilder.PushChild(model_core.Patch(e, entry)); err != nil {
					return PatchedPackageTargetsValue[TMetadata]{}, err
				}
			} else {
				// Subtree overlaps with other lists.
				// Merge its children.
				var err error
				children, err = model_parser.Dereference(ctx, analysisReaders.PackageValueTarget, model_core.Nested(entry, parent.Reference))
				if err != nil {
					return PatchedPackageTargetsValue[TMetadata]{}, err
				}
			}
		default:
			return PatchedPackageTargetsValue[TMetadata]{}, errors.New("invalid target level type")
		}

		if nonEmpty, err := cursor.advance(children); err != nil {
			return PatchedPackageTargetsValue[TMetadata]{}, err
		} else if nonEmpty {
			heap.Push(&cursors, cursor)
		}
	}

	targetsList, err := treeBuilder.FinalizeList()
	if err != nil {
		return PatchedPackageTargetsValue[TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_analysis_pb.PackageTargets_Value{
			Targets: targetsList.Message,
		},
		targetsList.Patcher,
	), nil
}

// PackageTargetsEnvironmentForTesting is used to generate mocks for
// testing ComputePackageTargetsValue().
type PackageTargetsEnvironmentForTesting PackageTargetsEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
