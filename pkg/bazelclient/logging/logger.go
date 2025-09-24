package logging

import (
	"os"

	"bonanza.build/pkg/bazelclient/arguments"
	"bonanza.build/pkg/bazelclient/formatted"

	"golang.org/x/term"
)

type Logger interface {
	Error(message formatted.Node)
	Fatal(message formatted.Node)
	Info(message formatted.Node)
	RemovePreviousLines(int)
}

func NewLoggerFromFlags(commonFlags *arguments.CommonFlags) Logger {
	w := os.Stderr
	var writeFormatted FormattedNodeWriter
	switch commonFlags.Color {
	case arguments.Color_Yes:
		writeFormatted = formatted.WriteVT100
	case arguments.Color_No:
		writeFormatted = formatted.WritePlainText
	case arguments.Color_Auto:
		if term.IsTerminal(int(w.Fd())) {
			writeFormatted = formatted.WriteVT100
		} else {
			writeFormatted = formatted.WritePlainText
		}
	}
	return NewConsoleLogger(w, writeFormatted)
}
