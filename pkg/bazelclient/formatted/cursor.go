package formatted

import (
	"fmt"
	"io"
)

type cursorUp struct {
	linesCount int
}

// CursorUp moves the cursor up by a specified number of lines.
func CursorUp(linesCount int) Node {
	return &cursorUp{
		linesCount: linesCount,
	}
}

func (cursorUp) writePlainText(w io.StringWriter) (int, error) {
	return 0, nil
}

func (n *cursorUp) writeVT100(w io.StringWriter, attributesState *vt100AttributesState) (int, error) {
	if n.linesCount > 0 {
		return w.WriteString(fmt.Sprintf("\x1b[%dA", n.linesCount))
	}
	return 0, nil
}
