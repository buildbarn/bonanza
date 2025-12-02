package filesystem

import "io"

// NewSectionWriter creates a new SectionWriter that starts writing data
// to the output, starting at offset zero.
func NewSectionWriter(w io.WriterAt) *SectionWriter {
	return &SectionWriter{w: w}
}

// SectionWriter provides an implementation of io.Writer on top of
// io.WriterAt. It is similar to io.SectionReader, but then for writes.
type SectionWriter struct {
	w           io.WriterAt
	offsetBytes int64
}

// GetOffsetBytes returns the offset at which the next call to Write()
// or WriteString() will start writing.
func (w *SectionWriter) GetOffsetBytes() int64 {
	return w.offsetBytes
}

// Write binary data to the underlying writer at the current offset, and
// progress the current offset.
func (w *SectionWriter) Write(p []byte) (int, error) {
	n, err := w.w.WriteAt(p, w.offsetBytes)
	w.offsetBytes += int64(n)
	return n, err
}

// WriteString writes string data to the underlying writer at the
// current offset, and progress the current offset.
func (w *SectionWriter) WriteString(s string) (int, error) {
	n, err := w.w.WriteAt([]byte(s), w.offsetBytes)
	w.offsetBytes += int64(n)
	return n, err
}
