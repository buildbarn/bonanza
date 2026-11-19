package formatted

import (
	"fmt"
	"io"
)

// vt100SingleAttributeState is used by WriteVT100() to keep track of
// any state pertaining to a single "Set Graphic Rendition" (SGR)
// attribute.
type vt100SingleAttributeState struct {
	current int
	desired []int
}

func newVT100AttributeState(v int) vt100SingleAttributeState {
	return vt100SingleAttributeState{
		current: v,
		desired: []int{v},
	}
}

func (sas *vt100SingleAttributeState) push(v int) {
	sas.desired = append(sas.desired, v)
}

func (sas *vt100SingleAttributeState) pop() {
	sas.desired = sas.desired[:len(sas.desired)-1]
}

func (sas *vt100SingleAttributeState) setCurrentToDesired(w io.StringWriter, wroteAttributes *bool) (int, error) {
	desired := sas.desired[len(sas.desired)-1]
	if desired == sas.current {
		return 0, nil
	}
	sas.current = desired

	leading := ";"
	if !*wroteAttributes {
		*wroteAttributes = true
		leading = "\x1b["
	}

	return w.WriteString(fmt.Sprintf("%s%d", leading, desired))
}

// vt100AttributesState is used by WriteVT100() to keep track of any
// state pertaining to all "Set Graphic Rendition" (SGR) attributes.
type vt100AttributesState struct {
	bold            vt100SingleAttributeState
	foregroundColor vt100SingleAttributeState
	backgroundColor vt100SingleAttributeState

	// The number of cells of text that still need to be written
	// with a background color, as requested by ProgressBar().
	backgroundCells int

	// If set, the number of cells of text that may still be
	// written, as requested by Truncate().
	truncating     bool
	remainingCells int
}

func (as *vt100AttributesState) setCurrentToDesired(w io.StringWriter) (int, error) {
	wroteAttributes := false
	nTotal, err := as.bold.setCurrentToDesired(w, &wroteAttributes)
	if err != nil {
		return nTotal, err
	}

	nPart, err := as.foregroundColor.setCurrentToDesired(w, &wroteAttributes)
	nTotal += nPart
	if err != nil {
		return nTotal, err
	}

	nPart, err = as.backgroundColor.setCurrentToDesired(w, &wroteAttributes)
	nTotal += nPart
	if err != nil {
		return nTotal, err
	}

	if wroteAttributes {
		nPart, err := w.WriteString("m")
		nTotal += nPart
		if err != nil {
			return nTotal, err
		}
	}
	return nTotal, nil
}

// WriteVT100 writes the text contained in a node, using VT100 style
// escape sequences for any formatting directives.
func WriteVT100(n Node, w io.StringWriter) (int, error) {
	attributesState := vt100AttributesState{
		bold:            newVT100AttributeState(22),
		foregroundColor: newVT100AttributeState(39),
		backgroundColor: newVT100AttributeState(49),
	}
	nTotal, err := n.writeVT100(w, &attributesState)
	if err != nil {
		return nTotal, err
	}
	nPart, err := attributesState.setCurrentToDesired(w)
	nTotal += nPart
	return nTotal, err
}
