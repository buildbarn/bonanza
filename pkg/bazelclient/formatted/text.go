package formatted

import (
	"fmt"
	"io"
	"unicode/utf8"
)

type text struct {
	body string
}

// Text causes a piece of text to be written in literal form.
func Text(body string) Node {
	return text{
		body: body,
	}
}

// Textf causes a piece of text containing string formatting directives
// to be written after performing substitutions.
func Textf(format string, args ...any) Node {
	return text{
		body: fmt.Sprintf(format, args...),
	}
}

func (n text) writePlainText(w io.StringWriter) (int, error) {
	return w.WriteString(n.body)
}

func (n text) writeVT100(w io.StringWriter, attributesState *vt100AttributesState) (int, error) {
	body := n.body
	if attributesState.truncating {
		body = truncateRunes(body, attributesState.remainingCells)
		attributesState.remainingCells -= utf8.RuneCountInString(body)
	}
	if body == "" {
		// Don't emit any attributes for empty text, as that
		// would needlessly interrupt progress bars.
		return 0, nil
	}
	nTotal := 0
	if attributesState.backgroundCells > 0 {
		// Part of the text needs to be written with a
		// background color to render a progress bar.
		splitIndex := len(body)
		cells := 0
		for i := range body {
			if cells == attributesState.backgroundCells {
				splitIndex = i
				break
			}
			cells++
		}
		attributesState.backgroundCells -= cells
		nPart, err := writeVT100WithBackground(w, attributesState, body[:splitIndex])
		nTotal += nPart
		if err != nil {
			return nTotal, err
		}
		body = body[splitIndex:]
		if body == "" {
			return nTotal, nil
		}
	}

	nPart, err := attributesState.setCurrentToDesired(w)
	nTotal += nPart
	if err != nil {
		return nTotal, err
	}
	nPart, err = w.WriteString(body)
	nTotal += nPart
	return nTotal, err
}
