package formatted

import (
	"io"
	"strings"
)

type progressBar struct {
	filledCells int
	base        Node
}

// ProgressBar renders text where the background color of the first
// cells is changed, so that the text itself acts as a progress bar. If
// the text is shorter than the number of cells that need to be filled,
// it is padded with spaces.
//
// Cells are counted in runes. This means that progress bars may be
// slightly off for text containing wide characters.
func ProgressBar(filledCells int, base Node) Node {
	return &progressBar{
		filledCells: filledCells,
		base:        base,
	}
}

func (n *progressBar) writePlainText(w io.StringWriter) (int, error) {
	return n.base.writePlainText(w)
}

func (n *progressBar) writeVT100(w io.StringWriter, attributesState *vt100AttributesState) (int, error) {
	if n.filledCells <= 0 {
		return n.base.writeVT100(w, attributesState)
	}

	previousBackgroundCells := attributesState.backgroundCells
	attributesState.backgroundCells = n.filledCells
	nTotal, err := n.base.writeVT100(w, attributesState)
	if err == nil && attributesState.backgroundCells > 0 {
		// Text was shorter than the progress bar. Pad it.
		var nPart int
		nPart, err = writeVT100WithBackground(w, attributesState, strings.Repeat(" ", attributesState.backgroundCells))
		nTotal += nPart
	}
	attributesState.backgroundCells = previousBackgroundCells
	return nTotal, err
}

// writeVT100WithBackground writes a string, using the background color
// that is used to render progress bars.
func writeVT100WithBackground(w io.StringWriter, attributesState *vt100AttributesState, s string) (int, error) {
	attributesState.backgroundColor.push(100)
	defer attributesState.backgroundColor.pop()
	nTotal, err := attributesState.setCurrentToDesired(w)
	if err != nil {
		return nTotal, err
	}
	nPart, err := w.WriteString(s)
	nTotal += nPart
	return nTotal, err
}
