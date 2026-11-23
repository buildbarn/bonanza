// Package canonicalzstd implements a Zstandard compressor whose output
// is fully determined by its input. Unlike general purpose Zstandard
// compressors, the output does not depend on the version or
// implementation of the compressor, making it suitable for use in
// content addressed storage. The output can be decompressed by any
// conforming Zstandard decoder.
//
// The format is specified in README.md.
package canonicalzstd

import (
	"encoding/binary"
	"slices"
	"sync"
)

const (
	maximumBlockSizeBytes = 128 * 1024

	blockTypeRaw        = 0
	blockTypeRLE        = 1
	blockTypeCompressed = 2
)

// MagicNumber of Zstandard frames, which Compress() emits at the start
// of every non-empty output.
var MagicNumber = [...]byte{0x28, 0xb5, 0x2f, 0xfd}

// appendFrameHeader appends a frame header that has the
// Single_Segment_Flag set and contains the Frame_Content_Size.
func appendFrameHeader(out []byte, contentSize int) []byte {
	const singleSegment = 1 << 5
	switch {
	case contentSize < 256:
		return append(out, singleSegment, byte(contentSize))
	case contentSize < 65536+256:
		return binary.LittleEndian.AppendUint16(append(out, 1<<6|singleSegment), uint16(contentSize-256))
	case uint64(contentSize) < 1<<32:
		return binary.LittleEndian.AppendUint32(append(out, 2<<6|singleSegment), uint32(contentSize))
	default:
		return binary.LittleEndian.AppendUint64(append(out, 3<<6|singleSegment), uint64(contentSize))
	}
}

func appendBlockHeader(out []byte, last bool, blockType, size int) []byte {
	v := blockType<<1 | size<<3
	if last {
		v |= 1
	}
	return append(out, byte(v), byte(v>>8), byte(v>>16))
}

// encoder holds memory that is reused across calls to Compress().
type encoder struct {
	mf        matchFinder
	sequences []sequence
	literals  []byte
	block     []byte
	out       []byte
	scratch   entropyScratch
}

var encoderPool = sync.Pool{
	New: func() any { return &encoder{} },
}

// Compress data, returning a canonical Zstandard frame. Empty input
// yields empty output.
func Compress(uncompressed []byte) []byte {
	if len(uncompressed) == 0 {
		return []byte{}
	}
	e := encoderPool.Get().(*encoder)
	defer encoderPool.Put(e)
	return e.compress(uncompressed)
}

func (e *encoder) compress(uncompressed []byte) []byte {
	out := appendFrameHeader(append(e.out[:0], MagicNumber[:]...), len(uncompressed))
	mf := &e.mf
	mf.reset(uncompressed)
	defer func() { mf.data = nil }()
	rep := initialRepeatedOffsets
	for start := 0; ; start += maximumBlockSizeBytes {
		end := min(start+maximumBlockSizeBytes, len(uncompressed))
		last := end == len(uncompressed)
		block := uncompressed[start:end]

		// RLE blocks are used when all bytes are identical and
		// the block is at least two bytes in size.
		if len(block) >= 2 {
			allEqual := true
			for _, b := range block[1:] {
				if b != block[0] {
					allEqual = false
					break
				}
			}
			if allEqual {
				out = appendBlockHeader(out, last, blockTypeRLE, len(block))
				out = append(out, block[0])
				if last {
					break
				}
				continue
			}
		}

		// Attempt to compress the block. Only use the result
		// if it's smaller than the original. Repeated offsets
		// are only updated by compressed blocks.
		newRep := rep
		e.sequences, e.literals = mf.parseBlock(uint32(start), uint32(end), &newRep, e.sequences[:0], e.literals[:0])
		e.block = e.scratch.appendLiteralsSection(e.block[:0], e.literals)
		e.block = e.scratch.appendSequencesSection(e.block, e.sequences)
		if len(e.block) < len(block) {
			out = appendBlockHeader(out, last, blockTypeCompressed, len(e.block))
			out = append(out, e.block...)
			rep = newRep
		} else {
			out = appendBlockHeader(out, last, blockTypeRaw, len(block))
			out = append(out, block...)
		}
		if last {
			break
		}
	}
	e.out = out
	return slices.Clone(out)
}
