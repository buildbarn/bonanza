package logging

import (
	"bytes"
	"io"
	"os"

	"bonanza.build/pkg/bazelclient/formatted"
)

type FormattedNodeWriter func(message formatted.Node, w io.StringWriter) (int, error)

type consoleLogger struct {
	w              io.Writer
	writeFormatted FormattedNodeWriter
}

func NewConsoleLogger(w io.Writer, writeFormatted FormattedNodeWriter) Logger {
	return &consoleLogger{
		w:              w,
		writeFormatted: writeFormatted,
	}
}

func (l *consoleLogger) Error(message formatted.Node) {
	var b bytes.Buffer
	l.writeFormatted(
		formatted.Join(
			formatted.Bold(formatted.Red(formatted.Text("ERROR: "))),
			message,
			formatted.Text("\n"),
		),
		&b,
	)
	l.w.Write(b.Bytes())
}

func (l *consoleLogger) Fatal(message formatted.Node) {
	l.Error(message)
	os.Exit(1)
}

func (l *consoleLogger) Info(message formatted.Node) {
	var b bytes.Buffer
	l.writeFormatted(
		formatted.Join(
			formatted.Green(formatted.Text("INFO: ")),
			message,
			formatted.Text("\n"),
		),
		&b,
	)
	l.w.Write(b.Bytes())
}

func (l *consoleLogger) RemovePreviousLines(linesCount int) {
	var b bytes.Buffer
	l.writeFormatted(
		formatted.Join(
			formatted.Text("\r"),
			formatted.CursorUp(linesCount),
			formatted.EraseDisplay,
		),
		&b,
	)
	l.w.Write(b.Bytes())
}
