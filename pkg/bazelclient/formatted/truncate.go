package formatted

import (
	"io"
	"strings"
	"unicode/utf8"
)

type truncate struct {
	maximumCells int
	base         Node
}

// Truncate text, so that it occupies at most a given number of cells.
// If the text needs to be truncated, its last cell is replaced by an
// ellipsis.
//
// Cells are counted in runes. This means that truncation may be
// slightly off for text containing wide characters.
func Truncate(maximumCells int, base Node) Node {
	return &truncate{
		maximumCells: max(maximumCells, 0),
		base:         base,
	}
}

func (n *truncate) fits() bool {
	var sb strings.Builder
	n.base.writePlainText(&sb)
	return utf8.RuneCountInString(sb.String()) <= n.maximumCells
}

func (n *truncate) writePlainText(w io.StringWriter) (int, error) {
	var sb strings.Builder
	n.base.writePlainText(&sb)
	s := sb.String()
	if utf8.RuneCountInString(s) <= n.maximumCells {
		return w.WriteString(s)
	}
	if n.maximumCells == 0 {
		return 0, nil
	}
	return w.WriteString(truncateRunes(s, n.maximumCells-1) + "…")
}

func (n *truncate) writeVT100(w io.StringWriter, attributesState *vt100AttributesState) (int, error) {
	if n.fits() {
		return n.base.writeVT100(w, attributesState)
	}
	if n.maximumCells == 0 {
		return 0, nil
	}

	// Leave room for the ellipsis. When nested inside another call
	// to Truncate(), also respect the limit imposed by that call.
	previousTruncating, previousRemainingCells := attributesState.truncating, attributesState.remainingCells
	budget := n.maximumCells - 1
	if previousTruncating {
		budget = min(budget, previousRemainingCells)
	}
	attributesState.truncating = true
	attributesState.remainingCells = budget
	nTotal, err := n.base.writeVT100(w, attributesState)
	writtenCells := budget - attributesState.remainingCells
	attributesState.truncating = previousTruncating
	attributesState.remainingCells = previousRemainingCells - writtenCells
	if err != nil {
		return nTotal, err
	}
	nPart, err := Text("…").writeVT100(w, attributesState)
	nTotal += nPart
	return nTotal, err
}

// truncateRunes returns the prefix of a string that contains at most a
// given number of runes.
func truncateRunes(s string, maximumRunes int) string {
	runes := 0
	for i := range s {
		if runes == maximumRunes {
			return s[:i]
		}
		runes++
	}
	return s
}
