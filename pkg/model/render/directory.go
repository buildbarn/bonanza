package render

import (
	"fmt"
	"slices"
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
// are displayed as links.
func renderDirectory(directory model_core.Message[*model_filesystem_pb.DirectoryContents, object.LocalReference]) Doc {
	return Table(
		[]TableColumn{
			{Name: "Type"},
			{Name: "Size", AlignRight: true},
			{Name: "Lines", AlignRight: true},
			{Name: "Path"},
		},
		renderDirectoryRows(directory, ""),
	)
}

func directoryErrorf(format string, args ...any) []Doc {
	return []Doc{Styled(RoleError, fmt.Sprintf(format, args...))}
}

func renderDirectoryRows(dirMessage model_core.Message[*model_filesystem_pb.DirectoryContents, object.LocalReference], path string) [][]Doc {
	var rows [][]Doc

	var names []string
	contents := map[string]any{}
	addEntry := func(name string, value any) {
		if _, ok := contents[name]; ok {
			rows = append(rows, directoryErrorf("duplicate entry %s", name))
		} else {
			contents[name] = value
			names = append(names, name)
		}
	}

	directory := dirMessage.Message
	for _, subdir := range directory.Directories {
		addEntry(subdir.Name, subdir)
	}
	switch leaves := directory.Leaves.(type) {
	case *model_filesystem_pb.DirectoryContents_LeavesExternal:
		addEntry("...", leaves.LeavesExternal)
	case *model_filesystem_pb.DirectoryContents_LeavesInline:
		for _, file := range leaves.LeavesInline.Files {
			addEntry(file.Name, file)
		}
		for _, symlink := range leaves.LeavesInline.Symlinks {
			addEntry(symlink.Name, symlink)
		}
	}

	if len(names) == 0 {
		// Directory has no contents. Render it as just itself.
		return append(rows, []Doc{Text("dr-x"), Text(""), Text(""), Text(path)})
	}

	slices.Sort(names)

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

	for _, name := range names {
		switch file := contents[name].(type) {
		case *model_filesystem_pb.DirectoryNode:
			switch contents := file.Directory.GetContents().(type) {
			case *model_filesystem_pb.Directory_ContentsExternal:
				rows = append(rows, []Doc{
					Text("dr-x"),
					Text(""),
					Text(""),
					Concat(
						getPathPrefix(path),
						Text(name+"/"),
						fieldReferenceLink(
							model_core.Nested(dirMessage, contents.ContentsExternal.Reference),
							contents.ContentsExternal, "reference",
							Text("..."),
						),
					),
				})
			case *model_filesystem_pb.Directory_ContentsInline:
				children := renderDirectoryRows(model_core.Nested(dirMessage, contents.ContentsInline), path+file.Name+"/")
				if len(children) > 0 {
					firstPrefix = true
				}
				rows = append(rows, children...)
			}

		case *model_filesystem_pb.LeavesReference:
			rows = append(rows, []Doc{
				Text("dr-x"),
				Text(""),
				Text(""),
				Concat(
					getPathPrefix(path),
					fieldReferenceLink(
						model_core.Nested(dirMessage, file.Reference),
						file, "reference",
						Text(name),
					),
				),
			})

		case *model_filesystem_pb.FileNode:
			mode := "-r--"
			if file.Properties.GetIsExecutable() {
				mode = "-r-x"
			}

			pathDoc := Text(file.Name)
			var totalSizeBytes uint64
			// Empty files are valid text files without any lines.
			linesDoc := Text("0")
			if contents := file.Properties.GetContents(); contents != nil {
				switch reference := contents.Level.(type) {
				case *model_filesystem_pb.FileContents_ChunkReference:
					pathDoc = fieldReferenceLink(
						model_core.Nested(dirMessage, reference.ChunkReference),
						contents, "chunk_reference",
						pathDoc,
					)
				case *model_filesystem_pb.FileContents_List_:
					pathDoc = fieldReferenceLink(
						model_core.Nested(dirMessage, reference.List.Reference),
						reference.List, "reference",
						pathDoc,
					)
				}
				totalSizeBytes = contents.TotalSizeBytes
				linesDoc = renderLines(utf8stat.Stat(contents.Utf8Stat))
			}
			rows = append(rows, []Doc{
				Text(mode),
				renderSize(totalSizeBytes),
				linesDoc,
				Concat(getPathPrefix(path), pathDoc),
			})

		case *model_filesystem_pb.SymlinkNode:
			rows = append(rows, []Doc{
				Text("lr-x"),
				Text(""),
				Text(""),
				Concat(
					getPathPrefix(path),
					Text(file.Name+" → "+file.Target),
				),
			})
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
