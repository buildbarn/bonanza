package render

import (
	"fmt"
	"strconv"
	"strings"

	"bonanza.build/pkg/encoding/utf8stat"
	model_core "bonanza.build/pkg/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"
)

// renderDirectory renders the contents of a directory as a table,
// similar to the output of "ls -lR". Subdirectories that are stored
// inline are expanded, while ones that are stored in separate objects
// are displayed as links. Nodes stored in separate objects are
// displayed as a single link.
//
// If partial is set, the entries may only form a part of a directory's
// B-tree (e.g., an object referenced by a DirectoryEntry.Parent). In
// that case subdirectory nodes are only matched with their contents if
// the number of nodes and contents are equal. Otherwise, contents are
// displayed separately, using their index as a name.
func renderDirectory(directory model_core.Message[[]*model_filesystem_pb.DirectoryEntry, object.LocalReference], partial bool) Doc {
	return Table(
		[]TableColumn{
			{Name: "Type"},
			{Name: "Size", AlignRight: true},
			{Name: "Lines", AlignRight: true},
			{Name: "Path"},
		},
		renderDirectoryRows(directory, "", partial),
	)
}

// externalDirectory is used by renderDirectoryRows to store a
// subdirectory whose contents are stored in another object. Multiple
// subdirectories may be stored in the same object.
type externalDirectory struct {
	parent *model_filesystem_pb.DirectoryEntry_Parent
	index  uint32
}

func directoryErrorf(format string, args ...any) []Doc {
	return []Doc{Styled(RoleError, fmt.Sprintf(format, args...))}
}

func renderDirectoryRows(dirMessage model_core.Message[[]*model_filesystem_pb.DirectoryEntry, object.LocalReference], path string, partial bool) [][]Doc {
	var rows [][]Doc
	entries := dirMessage.Message
	if len(entries) == 0 {
		// Directory has no contents. Render it as just itself.
		return append(rows, []Doc{Text("dr-x"), Text(""), Text(""), Text(path)})
	}

	// Determine where the contents of each subdirectory are
	// stored. Subdirectories are identified by their index in the
	// first section of the B-tree.
	knownDirectoryNames := map[int]bool{}
	inlineDirectories := map[int][]*model_filesystem_pb.DirectoryEntry{}
	externalDirectories := map[int]externalDirectory{}
	directoryNodesSeen, directoryContentsSeen := 0, 0
	for _, entry := range entries {
		switch entry.WhichLevel() {
		case model_filesystem_pb.DirectoryEntry_Parent_case:
			directoryNodesSeen += int(entry.GetParent().GetDirectoryNodesCount())
			for i := range entry.GetParent().GetDirectoryContentsCount() {
				externalDirectories[directoryContentsSeen] = externalDirectory{
					parent: entry.GetParent(),
					index:  i,
				}
				directoryContentsSeen++
			}
		case model_filesystem_pb.DirectoryEntry_Node_case:
			if entry.GetNode().GetDirectory() != nil {
				knownDirectoryNames[directoryNodesSeen] = true
				directoryNodesSeen++
			}
		case model_filesystem_pb.DirectoryEntry_DirectoryContents_case:
			inlineDirectories[directoryContentsSeen] = entry.GetDirectoryContents().GetEntries()
			directoryContentsSeen++
		}
	}

	// If the entries only form a part of a directory's B-tree,
	// the subdirectory nodes and contents stored in it don't
	// necessarily correspond to each other. Assume the entries form
	// a complete directory if their counts match.
	if partial {
		if directoryNodesSeen == directoryContentsSeen {
			partial = false
		} else {
			clear(knownDirectoryNames)
		}
	}

	// Only display the path of the directory in full for the first
	// entry. For subsequent entries, dim it to make the names of
	// the entries stand out.
	firstPrefix := true
	getPathPrefix := func(path string) Doc {
		if firstPrefix {
			firstPrefix = false
			return Text(path)
		}
		return Styled(RoleDim, path)
	}

	renderSubdirectory := func(name string, directoryIndex int) {
		if contents, ok := inlineDirectories[directoryIndex]; ok {
			children := renderDirectoryRows(model_core.Nested(dirMessage, contents), path+name+"/", false)
			if len(children) > 0 {
				firstPrefix = true
			}
			rows = append(rows, children...)
		} else if external, ok := externalDirectories[directoryIndex]; ok {
			rows = append(rows, []Doc{
				Text("dr-x"),
				Text(""),
				Text(""),
				Concat(
					getPathPrefix(path),
					fieldReferenceLink(
						model_core.Nested(dirMessage, external.parent.GetReference()),
						external.parent, "reference",
						Text(name+"/"),
					),
					// The index of the subdirectory's
					// contents within the part of the
					// B-tree stored in the linked
					// object, to make it easier to find.
					Styled(RoleDim, fmt.Sprintf(" (index %d)", external.index)),
				),
			})
		} else {
			rows = append(rows, directoryErrorf("contents of subdirectory %s are missing", name))
		}
	}

	directoryIndex := 0
	for _, entry := range entries {
		switch entry.WhichLevel() {
		case model_filesystem_pb.DirectoryEntry_Parent_case:
			// Nodes stored in another object.
			if nodesCount := entry.GetParent().GetLeafNodesCount() + entry.GetParent().GetDirectoryNodesCount(); nodesCount > 0 {
				rows = append(rows, []Doc{
					Text(""),
					Text(""),
					Text(""),
					Concat(
						getPathPrefix(path),
						fieldReferenceLink(
							model_core.Nested(dirMessage, entry.GetParent().GetReference()),
							entry.GetParent(), "reference",
							Text(fmt.Sprintf("%s… (%d entries)", entry.GetParent().GetFirstName(), nodesCount)),
						),
					),
				})
			}
			directoryIndex += int(entry.GetParent().GetDirectoryNodesCount())

		case model_filesystem_pb.DirectoryEntry_Node_case:
			node := entry.GetNode()
			switch node.WhichKind() {
			case model_filesystem_pb.DirectoryEntry_Node_Directory_case:
				if partial {
					// The contents of the subdirectory
					// can't be identified.
					rows = append(rows, []Doc{
						Text("dr-x"),
						Text(""),
						Text(""),
						Concat(getPathPrefix(path), Text(node.GetName()+"/")),
					})
				} else {
					renderSubdirectory(node.GetName(), directoryIndex)
				}
				directoryIndex++

			case model_filesystem_pb.DirectoryEntry_Node_File_case:
				mode := "-r--"
				if node.GetFile().GetIsExecutable() {
					mode = "-r-x"
				}

				pathDoc := Text(node.GetName())
				var totalSizeBytes uint64
				// Empty files are valid text files without
				// any lines.
				linesDoc := Text("0")
				if contents := node.GetFile().GetContents(); contents != nil {
					switch contents.WhichLevel() {
					case model_filesystem_pb.FileContents_ChunkReference_case:
						pathDoc = fieldReferenceLink(
							model_core.Nested(dirMessage, contents.GetChunkReference()),
							contents, "chunk_reference",
							pathDoc,
						)
					case model_filesystem_pb.FileContents_List_case:
						pathDoc = fieldReferenceLink(
							model_core.Nested(dirMessage, contents.GetList().GetReference()),
							contents.GetList(), "reference",
							pathDoc,
						)
					}
					totalSizeBytes = contents.GetTotalSizeBytes()
					linesDoc = renderLines(utf8stat.Stat(contents.GetUtf8Stat()))
				}
				rows = append(rows, []Doc{
					Text(mode),
					renderSize(totalSizeBytes),
					linesDoc,
					Concat(getPathPrefix(path), pathDoc),
				})

			case model_filesystem_pb.DirectoryEntry_Node_Symlink_case:
				rows = append(rows, []Doc{
					Text("lr-x"),
					Text(""),
					Text(""),
					Concat(
						getPathPrefix(path),
						Text(node.GetName()+" → "+node.GetSymlink().GetTarget()),
					),
				})
			}
		}
	}

	// Subdirectories whose nodes are stored in another object,
	// while their contents are stored in this object or are
	// referenced by it. Their names are unknown.
	for i := 0; i < directoryContentsSeen; i++ {
		if !knownDirectoryNames[i] {
			renderSubdirectory(fmt.Sprintf("#%d", i), i)
		}
	}
	return rows
}

// renderSize renders a size in bytes in human-readable form. If the
// size is rounded, the exact size is provided as a title.
func renderSize(bytes uint64) Doc {
	size := Text(formatSize(bytes))
	if bytes < 1024 {
		return size
	}
	return Title(formatThousands(bytes)+" bytes", size)
}

// renderLines renders the number of lines in a file. Nothing is
// rendered for files that are not valid UTF-8 text, as the notion of
// lines is meaningless for binary data.
func renderLines(stat utf8stat.Stat) Doc {
	if !stat.IsValidFile() {
		return Text("")
	}
	return Text(formatThousands(stat.LineBreaks() + 1))
}

// formatThousands formats a number with thousands separators.
func formatThousands(n uint64) string {
	digits := strconv.FormatUint(n, 10)
	var sb strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			sb.WriteByte(',')
		}
		sb.WriteRune(digit)
	}
	return sb.String()
}

// formatSize formats a size in bytes in human-readable form, using
// binary prefixes. Sizes of 1 KiB and above are displayed with a single
// decimal. Sizes below 1 KiB are padded, so that both the numbers and
// the units line up when sizes are displayed in a right aligned column.
func formatSize(bytes uint64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d   B", bytes)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	size := float64(bytes) / 1024
	unit := 0
	for size >= 1024 && unit+1 < len(units) {
		size /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", size, units[unit])
}
