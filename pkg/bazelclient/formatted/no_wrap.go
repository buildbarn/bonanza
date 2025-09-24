package formatted

import (
	"io"
)

type noWrap struct {
	base Node
}

func NoWrap(base Node) Node {
	return &noWrap{
		base: base,
	}
}

func (n *noWrap) writePlainText(w io.StringWriter) (int, error) {
	return n.base.writePlainText(w)
}

func (n *noWrap) writeVT100(w io.StringWriter, attributesState *vt100AttributesState) (int, error) {
	nTotal, err := w.WriteString("\x1b[?7l")
	if err != nil {
		return nTotal, err
	}

	nPart, err := n.base.writeVT100(w, attributesState)
	nTotal += nPart
	if err != nil {
		return nTotal, err
	}

	nPart, err = w.WriteString("\x1b[?7l")
	nTotal += nPart
	return nTotal, err
}
