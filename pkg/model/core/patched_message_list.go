package core

// PatchedMessageList is a list of PatchedMessage objects, which have
// not been merged into a single PatchedMessage yet.
type PatchedMessageList[TMessage any, TMetadata ReferenceMetadata] []PatchedMessage[TMessage, TMetadata]

// Discard all messages contained in the list.
func (l PatchedMessageList[TMessage, TMetadata]) Discard() {
	for i := range l {
		l[i].Discard()
	}
}

// Merge all the messages in the list, so that references contained in
// all messages are managed by a single ReferenceMessagePatcher.
func (l PatchedMessageList[TMessage, TMetadata]) Merge() PatchedMessage[[]TMessage, TMetadata] {
	return MustBuildPatchedMessage(func(patcher *ReferenceMessagePatcher[TMetadata]) []TMessage {
		mergedMessages := make([]TMessage, 0, len(l))
		for _, entry := range l {
			mergedMessages = append(mergedMessages, entry.Merge(patcher))
		}
		return mergedMessages
	})
}
