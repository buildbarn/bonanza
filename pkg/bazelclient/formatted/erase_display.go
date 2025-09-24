package formatted

import (
	"io"
)

type eraseDisplay struct{}

// EraseDisplay erases all contents from the cursor through the end of
// the display.
var EraseDisplay Node = eraseDisplay{}

func (eraseDisplay) writePlainText(w io.StringWriter) (int, error) {
	return 0, nil
}

func (eraseDisplay) writeVT100(w io.StringWriter, attributesState *vt100AttributesState) (int, error) {
	return w.WriteString("\x1b[J")
}
