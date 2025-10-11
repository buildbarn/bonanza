package diff

import (
	"io"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

// ByteStringWriter is used by ReplaceTextFragments() to either copy
// unmodified binary data from an input file, or write replacement
// strings to an output file.
type ByteStringWriter interface {
	io.Writer
	io.StringWriter
}

// ReplaceTextFragments performs substitutions on an input file,
// replacing the contents of fragments in a diff file with the parts
// that need to be added. The offsets at which substitutions need to be
// performed are provided in the form of a list that can be obtained by
// calling FindTextFragmentOffsetsBytes().
func ReplaceTextFragments(dst ByteStringWriter, src io.Reader, fragments []*gitdiff.TextFragment, offsetsBytes []int64) error {
	for i, fragment := range fragments {
		if _, err := io.CopyN(dst, src, offsetsBytes[i]); err != nil {
			return err
		}
		skipBytes := int64(0)
		for _, line := range fragment.Lines {
			if line.Op == gitdiff.OpContext || line.Op == gitdiff.OpDelete {
				skipBytes += int64(len(line.Line))
			}
			if line.Op == gitdiff.OpContext || line.Op == gitdiff.OpAdd {
				if _, err := dst.WriteString(line.Line); err != nil {
					return err
				}
			}
		}
		if _, err := io.CopyN(io.Discard, src, skipBytes); err != nil {
			return err
		}
	}
	_, err := io.Copy(dst, src)
	return err
}
