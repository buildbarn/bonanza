package canonicalzstd

import (
	"bytes"
	"encoding/binary"

	"github.com/klauspost/compress/zstd"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var decoder *zstd.Decoder

func init() {
	var err error
	decoder, err = zstd.NewReader(
		nil,
		zstd.WithDecoderConcurrency(0),
	)
	if err != nil {
		panic(err)
	}
}

// getContentSize extracts the Frame_Content_Size from the frame
// header, requiring that the frame header has the properties that are
// mandated by the canonical encoding.
func getContentSize(compressed []byte) (uint64, int, error) {
	if len(compressed) < 1 {
		return 0, 0, status.Error(codes.InvalidArgument, "Frame header descriptor missing")
	}
	descriptor := compressed[0]
	if descriptor&0x3f != 1<<5 {
		return 0, 0, status.Errorf(codes.InvalidArgument, "Frame header descriptor 0x%02x does not denote a single segment frame without checksum or dictionary", descriptor)
	}
	switch descriptor >> 6 {
	case 0:
		if len(compressed) < 2 {
			break
		}
		return uint64(compressed[1]), 2, nil
	case 1:
		if len(compressed) < 3 {
			break
		}
		return uint64(binary.LittleEndian.Uint16(compressed[1:])) + 256, 3, nil
	case 2:
		if len(compressed) < 5 {
			break
		}
		return uint64(binary.LittleEndian.Uint32(compressed[1:])), 5, nil
	default:
		if len(compressed) < 9 {
			break
		}
		return binary.LittleEndian.Uint64(compressed[1:]), 9, nil
	}
	return 0, 0, status.Error(codes.InvalidArgument, "Frame content size truncated")
}

// validateBlocks checks that the blocks following the frame header
// form exactly one frame that ends at the end of the input. This is
// necessary, because the decoder would otherwise decompress any
// frames that follow, which would allow small inputs to decompress to
// arbitrarily large outputs.
//
// The decoder itself ensures that the output of compressed blocks does
// not exceed the frame content size.
func validateBlocks(blocks []byte, contentSize uint64) error {
	maximumBlockSize := min(contentSize, maximumBlockSizeBytes)
	var rawAndRLESize uint64
	for {
		if len(blocks) < 3 {
			return status.Error(codes.InvalidArgument, "Block header truncated")
		}
		header := uint32(blocks[0]) | uint32(blocks[1])<<8 | uint32(blocks[2])<<16
		blocks = blocks[3:]
		last := header&1 != 0
		blockType := (header >> 1) & 3
		blockSize := uint64(header >> 3)
		if blockSize > maximumBlockSize {
			return status.Errorf(codes.InvalidArgument, "Block size of %d bytes exceeds the maximum of %d bytes", blockSize, maximumBlockSize)
		}

		var payloadSize uint64
		switch blockType {
		case blockTypeRaw:
			payloadSize = blockSize
			rawAndRLESize += blockSize
		case blockTypeRLE:
			payloadSize = 1
			rawAndRLESize += blockSize
		case blockTypeCompressed:
			payloadSize = blockSize
		default:
			return status.Error(codes.InvalidArgument, "Block has a reserved block type")
		}
		if rawAndRLESize > contentSize {
			return status.Errorf(codes.InvalidArgument, "Blocks decompress to more than the frame content size of %d bytes", contentSize)
		}
		if payloadSize > uint64(len(blocks)) {
			return status.Error(codes.InvalidArgument, "Block contents truncated")
		}
		blocks = blocks[payloadSize:]

		if last {
			if len(blocks) > 0 {
				return status.Errorf(codes.InvalidArgument, "Input contains %d bytes of trailing data after the last block", len(blocks))
			}
			return nil
		}
	}
}

// Decompress data that was compressed using Compress().
//
// This function accepts empty input, which decompresses to empty
// output, and any valid single segment Zstandard frame. Input must
// consist of exactly one frame. It does not validate that the input is
// canonical.
func Decompress(compressed []byte, maximumSizeBytes uint32) ([]byte, error) {
	if len(compressed) == 0 {
		return []byte{}, nil
	}
	if !bytes.HasPrefix(compressed, MagicNumber[:]) {
		return nil, status.Error(codes.InvalidArgument, "Input does not start with the Zstandard frame magic number")
	}
	contentSize, headerSize, err := getContentSize(compressed[len(MagicNumber):])
	if err != nil {
		return nil, err
	}
	if contentSize > uint64(maximumSizeBytes) {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"Decompressed data is %d bytes in size, which exceeds the permitted maximum of %d bytes",
			contentSize,
			maximumSizeBytes,
		)
	}
	if err := validateBlocks(compressed[len(MagicNumber)+headerSize:], contentSize); err != nil {
		return nil, err
	}

	decompressed, err := decoder.DecodeAll(compressed, make([]byte, 0, contentSize))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Failed to decompress data: %s", err)
	}
	if uint64(len(decompressed)) != contentSize {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"Size of decompressed output is %d bytes, which differs from frame content size of %d bytes",
			len(decompressed),
			contentSize,
		)
	}
	return decompressed, nil
}
