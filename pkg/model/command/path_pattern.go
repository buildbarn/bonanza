package command

import (
	"context"
	"iter"
	"maps"
	"slices"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_parser "bonanza.build/pkg/model/parser"
	model_command_pb "bonanza.build/pkg/proto/model/command"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetPathPatternWithChildren returns a PathPattern message that either
// has its children inlined into it, or referenced as an external
// object.
func GetPathPatternWithChildren[TMetadata model_core.ReferenceMetadata](
	children model_core.PatchedMessage[*model_command_pb.PathPattern_Children, TMetadata],
	externalObject *model_core.Decodable[model_core.MetadataEntry[TMetadata]],
	patcher *model_core.ReferenceMessagePatcher[TMetadata],
) *model_command_pb.PathPattern {
	if children.Message == nil {
		return &model_command_pb.PathPattern{}
	}
	if externalObject == nil {
		return &model_command_pb.PathPattern{
			Children: &model_command_pb.PathPattern_ChildrenInline{
				ChildrenInline: children.Message,
			},
		}
	}
	return &model_command_pb.PathPattern{
		Children: &model_command_pb.PathPattern_ChildrenExternal{
			ChildrenExternal: patcher.AddDecodableReference(*externalObject),
		},
	}
}

// GetPathPatternInlineCandidate creates a candidate for either inlining
// a path pattern child, or storing it externally. This candidate can be
// provided to inlinedtree.Build() to construct a PathPattern, which may
// get spread out across multiple objects if it gets too big.
func GetPathPatternInlineCandidate[TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	name string,
	grandChildren model_core.PatchedMessage[*model_command_pb.PathPattern_Children, TMetadata],
	encoder model_encoding.DeterministicBinaryEncoder,
	objectCapturer model_core.CreatedObjectCapturer[TMetadata],
) inlinedtree.Candidate[*model_command_pb.PathPattern_Children, TMetadata] {
	return inlinedtree.Candidate[*model_command_pb.PathPattern_Children, TMetadata]{
		ExternalMessage: model_core.ProtoToBinaryMarshaler(grandChildren),
		Encoder:         encoder,
		ParentAppender: inlinedtree.Capturing(ctx, objectCapturer, func(
			children model_core.PatchedMessage[*model_command_pb.PathPattern_Children, TMetadata],
			externalObject *model_core.Decodable[model_core.MetadataEntry[TMetadata]],
		) {
			children.Message.Children = append(children.Message.Children, &model_command_pb.PathPattern_Child{
				Name:    name,
				Pattern: GetPathPatternWithChildren(grandChildren, externalObject, children.Patcher),
			})
		}),
	}
}

// PrependDirectoryToPathPatternChildren prepends a single pathname
// component to a PathPattern.
//
// One use case for this is when a target action's comand is computed. A
// PathPattern will already be present, but it does not yet have the
// "bazel-out/${configuration}/bin" path prefix.
func PrependDirectoryToPathPatternChildren[TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	name string,
	grandChildren model_core.PatchedMessage[*model_command_pb.PathPattern_Children, TMetadata],
	encoder model_encoding.DeterministicBinaryEncoder,
	inlinedTreeOptions *inlinedtree.Options,
	objectCapturer model_core.CreatedObjectCapturer[TMetadata],
) (model_core.PatchedMessage[*model_command_pb.PathPattern_Children, TMetadata], error) {
	return inlinedtree.Build(
		inlinedtree.CandidateList[*model_command_pb.PathPattern_Children, TMetadata]{
			GetPathPatternInlineCandidate(ctx, name, grandChildren, encoder, objectCapturer),
		},
		inlinedTreeOptions,
	)
}

// PathPatternSet is a set of relative pathname strings that should be
// captured by a remote worker after execution of an action completes.
type PathPatternSet[TMetadata model_core.ReferenceMetadata] struct {
	children map[string]*PathPatternSet[TMetadata]
	included bool
}

// Add a relative pathname string to the set.
func (s *PathPatternSet[TMetadata]) Add(path iter.Seq[string]) {
	for component := range path {
		sChild, ok := s.children[component]
		if !ok {
			if s.children == nil {
				s.children = map[string]*PathPatternSet[TMetadata]{}
			}
			sChild = &PathPatternSet[TMetadata]{}
			s.children[component] = sChild
		}
		s = sChild
	}
	s.included = true
}

// ToProto converts the set of relative pathname strings contained in
// the set to a PathPattern message that can be embedded in a Command
// message, which is to be processed by a remote worker.
func (s *PathPatternSet[TMetadata]) ToProto(
	ctx context.Context,
	encoder model_encoding.DeterministicBinaryEncoder,
	inlinedTreeOptions *inlinedtree.Options,
	objectCapturer model_core.CreatedObjectCapturer[TMetadata],
) (model_core.PatchedMessage[*model_command_pb.PathPattern_Children, TMetadata], error) {
	if s.included {
		return model_core.NewSimplePatchedMessage[TMetadata]((*model_command_pb.PathPattern_Children)(nil)), nil
	}

	if len(s.children) == 0 {
		panic("leaf path should have been included in the path pattern set")
	}
	inlineCandidates := make(inlinedtree.CandidateList[*model_command_pb.PathPattern_Children, TMetadata], 0, len(s.children))
	for _, name := range slices.Sorted(maps.Keys(s.children)) {
		grandChildren, err := s.children[name].ToProto(ctx, encoder, inlinedTreeOptions, objectCapturer)
		if err != nil {
			return model_core.PatchedMessage[*model_command_pb.PathPattern_Children, TMetadata]{}, err
		}
		inlineCandidates = append(inlineCandidates, GetPathPatternInlineCandidate(ctx, name, grandChildren, encoder, objectCapturer))
	}
	return inlinedtree.Build(inlineCandidates, inlinedTreeOptions)
}

// PathPatternGetChildren returns the list of children contained in a
// PathPattern entry. This either causes it to return children that are
// inlined or stored externally.
func PathPatternGetChildren[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, *model_command_pb.PathPattern_Children],
	pathPattern model_core.Message[*model_command_pb.PathPattern, TReference],
) (model_core.Message[*model_command_pb.PathPattern_Children, TReference], error) {
	switch children := pathPattern.Message.Children.(type) {
	case *model_command_pb.PathPattern_ChildrenExternal:
		return model_parser.Dereference(ctx, reader, model_core.Nested(pathPattern, children.ChildrenExternal))
	case *model_command_pb.PathPattern_ChildrenInline:
		return model_core.Nested(pathPattern, children.ChildrenInline), nil
	case nil:
		// No children present.
		return model_core.Message[*model_command_pb.PathPattern_Children, TReference]{}, nil
	default:
		return model_core.Message[*model_command_pb.PathPattern_Children, TReference]{}, status.Error(codes.InvalidArgument, "Path pattern has unknown children")
	}
}
