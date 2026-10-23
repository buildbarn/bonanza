package formatted

import (
	"io"
)

type attr struct {
	base Node
}

func (n attr) writePlainText(w io.StringWriter) (int, error) {
	return n.base.writePlainText(w)
}

type bold struct {
	attr
}

// Bold renders text with bold mode enabled.
func Bold(base Node) Node {
	return bold{
		attr: attr{
			base: base,
		},
	}
}

func (n bold) writeVT100(w io.StringWriter, attributesState *vt100AttributesState) (int, error) {
	attributesState.bold.push(1)
	defer attributesState.bold.pop()
	return n.base.writeVT100(w, attributesState)
}

type foregroundColor struct {
	attr
	color int
}

// Red renders text with a red foreground color.
func Red(base Node) Node {
	return &foregroundColor{
		attr: attr{
			base: base,
		},
		color: 31,
	}
}

// Green renders text with a green foreground color.
func Green(base Node) Node {
	return &foregroundColor{
		attr: attr{
			base: base,
		},
		color: 32,
	}
}

// Yellow renders text with a yellow foreground color.
func Yellow(base Node) Node {
	return &foregroundColor{
		attr: attr{
			base: base,
		},
		color: 33,
	}
}

// Blue renders text with a blue foreground color.
func Blue(base Node) Node {
	return &foregroundColor{
		attr: attr{
			base: base,
		},
		color: 34,
	}
}

// Magenta renders text with a magenta foreground color.
func Magenta(base Node) Node {
	return &foregroundColor{
		attr: attr{
			base: base,
		},
		color: 35,
	}
}

// Cyan renders text with a cyan foreground color.
func Cyan(base Node) Node {
	return &foregroundColor{
		attr: attr{
			base: base,
		},
		color: 36,
	}
}

// Gray renders text with a gray ("bright black") foreground color.
func Gray(base Node) Node {
	return &foregroundColor{
		attr: attr{
			base: base,
		},
		color: 90,
	}
}

func (n *foregroundColor) writeVT100(w io.StringWriter, attributesState *vt100AttributesState) (int, error) {
	attributesState.foregroundColor.push(n.color)
	defer attributesState.foregroundColor.pop()
	return n.base.writeVT100(w, attributesState)
}
