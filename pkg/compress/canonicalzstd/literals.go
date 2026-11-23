package canonicalzstd

import (
	"encoding/binary"
	"slices"
)

const (
	literalsBlockTypeRaw        = 0
	literalsBlockTypeRLE        = 1
	literalsBlockTypeCompressed = 2

	maximumHuffmanCodeLength = 11
)

type packageMergeNode struct {
	weight      uint64
	symbol      int
	left, right int32
}

// huffmanScratch holds memory that is reused when Huffman coding
// literals.
type huffmanScratch struct {
	keys    []uint64
	nodes   []packageMergeNode
	current []int32
	merged  []int32
	stack   []int32

	weightNorm         []int16
	weightTables       [2]fseTable
	weightDescriptions [2][]byte
	treeDescription    []byte
	fseTreeDescription []byte
	compressed         []byte
}

// computeHuffmanCodeLengths computes optimal length-limited prefix
// code lengths using the package-merge algorithm, as specified in
// section "Huffman code lengths" of README.md. At least
// two counts must be non-zero.
func (hs *huffmanScratch) computeHuffmanCodeLengths(counts *[256]uint32, lengths *[256]uint8) {
	// Leaves, sorted by count, then by symbol value.
	keys := hs.keys[:0]
	for symbol, count := range counts {
		if count > 0 {
			keys = append(keys, uint64(count)<<8|uint64(symbol))
		}
	}
	slices.Sort(keys)
	hs.keys = keys
	nodes := hs.nodes[:0]
	for _, key := range keys {
		nodes = append(nodes, packageMergeNode{weight: key >> 8, symbol: int(key & 0xff), left: -1})
	}
	leafCount := int32(len(nodes))

	current := hs.current[:0]
	for i := int32(0); i < leafCount; i++ {
		current = append(current, i)
	}
	merged := hs.merged
	for level := 1; level < maximumHuffmanCodeLength; level++ {
		// Pair up adjacent items into packages, and merge them
		// with the leaves. On ties, leaves come first.
		merged = merged[:0]
		leaf := int32(0)
		for i := 0; i+1 < len(current); i += 2 {
			packageWeight := nodes[current[i]].weight + nodes[current[i+1]].weight
			for leaf < leafCount && nodes[leaf].weight <= packageWeight {
				merged = append(merged, leaf)
				leaf++
			}
			nodes = append(nodes, packageMergeNode{
				weight: packageWeight,
				symbol: -1,
				left:   current[i],
				right:  current[i+1],
			})
			merged = append(merged, int32(len(nodes)-1))
		}
		for ; leaf < leafCount; leaf++ {
			merged = append(merged, leaf)
		}
		current, merged = merged, current
	}
	hs.nodes, hs.current, hs.merged = nodes, current, merged

	// The code length of each symbol is equal to the number of
	// times it occurs within the first 2n-2 items.
	*lengths = [256]uint8{}
	stack := append(hs.stack[:0], current[:2*leafCount-2]...)
	for len(stack) > 0 {
		n := &nodes[stack[len(stack)-1]]
		stack = stack[:len(stack)-1]
		if n.symbol >= 0 {
			lengths[n.symbol]++
		} else {
			stack = append(stack, n.left, n.right)
		}
	}
	hs.stack = stack
}

func getRawOrRLELiteralsHeaderSize(size int) int {
	if size < 1<<5 {
		return 1
	}
	if size < 1<<12 {
		return 2
	}
	return 3
}

func appendRawOrRLELiteralsHeader(out []byte, blockType, size int) []byte {
	if size < 1<<5 {
		return append(out, byte(blockType|size<<3))
	}
	if size < 1<<12 {
		return binary.LittleEndian.AppendUint16(out, uint16(blockType|1<<2|size<<4))
	}
	v := blockType | 3<<2 | size<<4
	return append(out, byte(v), byte(v>>8), byte(v>>16))
}

// appendFSECompressedHuffmanWeights appends a Huffman tree description
// that uses FSE compression. As decoders cannot reliably determine
// the number of weights for streams containing fewer than three
// weights, or for distributions consisting of a single symbol,
// these are not permitted.
func (es *entropyScratch) appendFSECompressedHuffmanWeights(out []byte, weights []uint8) ([]byte, bool) {
	var counts [maximumHuffmanCodeLength + 1]uint32
	maximumWeight := uint8(0)
	distinctWeights := 0
	for _, weight := range weights {
		if counts[weight] == 0 {
			distinctWeights++
		}
		counts[weight]++
		maximumWeight = max(maximumWeight, weight)
	}
	if len(weights) < 3 || distinctWeights < 2 {
		return out, false
	}

	hs := &es.huffmanScratch
	best := -1
	for i, tableLog := range []uint{minimumFSETableLog, 6} {
		norm := es.normalizeCounts(hs.weightNorm, counts[:maximumWeight+1], tableLog)
		hs.weightNorm = norm
		t := &hs.weightTables[i]
		t.build(norm, tableLog, &es.fseScratch)
		w := bitWriter{out: appendFSETableDescription(hs.weightDescriptions[i][:0], norm, tableLog)}

		// Weights are encoded using two interleaved states.
		// Even weights use the first state, odd weights use
		// the second state.
		n := len(weights)
		var states [2]uint32
		states[(n-1)%2] = t.initialState[weights[n-1]]
		states[(n-2)%2] = t.initialState[weights[n-2]]
		for i := n - 3; i >= 0; i-- {
			t.encode(&w, &states[i%2], weights[i])
		}
		t.flush(&w, states[1])
		t.flush(&w, states[0])
		description := w.close()
		hs.weightDescriptions[i] = description
		if len(description) < 128 && (best < 0 || len(description) < len(hs.weightDescriptions[best])) {
			best = i
		}
	}
	if best < 0 {
		return out, false
	}
	description := hs.weightDescriptions[best]
	return append(append(out, byte(len(description))), description...), true
}

func appendHuffmanStream(out, literals []byte, codes *[256]uint16, lengths *[256]uint8) []byte {
	// Encode two literals at a time. Codes are at most 11 bits,
	// so up to 22 bits are added to at most 31 pending bits.
	var acc uint64
	var nBits uint
	i := len(literals)
	for ; i >= 2; i -= 2 {
		l0, l1 := literals[i-1], literals[i-2]
		acc |= uint64(codes[l0]) << nBits
		nBits += uint(lengths[l0])
		acc |= uint64(codes[l1]) << nBits
		nBits += uint(lengths[l1])
		if nBits >= 32 {
			out = binary.LittleEndian.AppendUint32(out, uint32(acc))
			acc >>= 32
			nBits -= 32
		}
	}
	w := bitWriter{out: out, acc: acc, nBits: nBits}
	if i == 1 {
		w.addBits(uint64(codes[literals[0]]), uint(lengths[literals[0]]))
	}
	return w.close()
}

// appendHuffmanCompressedLiterals appends a literals section of type
// Compressed_Literals_Block. False is returned if the literals
// cannot be represented in this form.
func (es *entropyScratch) appendHuffmanCompressedLiterals(out, literals []byte) ([]byte, bool) {
	var counts [256]uint32
	distinctLiterals := 0
	for _, l := range literals {
		if counts[l] == 0 {
			distinctLiterals++
		}
		counts[l]++
	}
	if distinctLiterals < 2 {
		return out, false
	}

	// Convert code lengths to weights.
	var lengths [256]uint8
	es.computeHuffmanCodeLengths(&counts, &lengths)
	maximumBits := slices.Max(lengths[:])
	var weights [256]uint8
	lastSymbol := 0
	for symbol, length := range lengths {
		if length > 0 {
			weights[symbol] = maximumBits + 1 - length
			lastSymbol = symbol
		}
	}

	// Assign prefix codes in the order mandated by the format:
	// by increasing weight, then by increasing symbol value.
	var codes [256]uint16
	nextPosition := 0
	for weight := uint8(1); weight <= maximumBits; weight++ {
		for symbol, w := range weights {
			if w == weight {
				codes[symbol] = uint16(nextPosition >> (weight - 1))
				nextPosition += 1 << (weight - 1)
			}
		}
	}

	// Huffman tree description. The weight of the last symbol is
	// implied. Prefer direct representation on ties.
	transmittedWeights := weights[:lastSymbol]
	hs := &es.huffmanScratch
	var description []byte
	if len(transmittedWeights) <= 128 {
		description = append(hs.treeDescription[:0], byte(127+len(transmittedWeights)))
		for i := 0; i < len(transmittedWeights); i += 2 {
			b := transmittedWeights[i] << 4
			if i+1 < len(transmittedWeights) {
				b |= transmittedWeights[i+1]
			}
			description = append(description, b)
		}
		hs.treeDescription = description
	}
	if fseDescription, ok := es.appendFSECompressedHuffmanWeights(hs.fseTreeDescription[:0], transmittedWeights); ok {
		hs.fseTreeDescription = fseDescription
		if description == nil || len(fseDescription) < len(description) {
			description = fseDescription
		}
	}
	if description == nil {
		return out, false
	}

	// Use a single stream if it fits in the smallest header.
	regeneratedSize := len(literals)
	if regeneratedSize < 1<<10 {
		streams := appendHuffmanStream(append(hs.compressed[:0], description...), literals, &codes, &lengths)
		hs.compressed = streams
		if compressedSize := len(streams); compressedSize < 1<<10 {
			v := literalsBlockTypeCompressed | regeneratedSize<<4 | compressedSize<<14
			out = append(out, byte(v), byte(v>>8), byte(v>>16))
			return append(out, streams...), true
		}
	}

	// Use four streams, preceded by a jump table.
	streams := append(append(hs.compressed[:0], description...), 0, 0, 0, 0, 0, 0)
	jumpTable := len(description)
	segmentSize := (regeneratedSize + 3) / 4
	for i := 0; i < 4; i++ {
		start := len(streams)
		streams = appendHuffmanStream(streams, literals[min(i*segmentSize, regeneratedSize):min((i+1)*segmentSize, regeneratedSize)], &codes, &lengths)
		if i < 3 {
			binary.LittleEndian.PutUint16(streams[jumpTable+2*i:], uint16(len(streams)-start))
		}
	}
	hs.compressed = streams

	compressedSize := len(streams)
	sizeFormat, sizeBits, headerSize := 1, 10, 3
	if regeneratedSize >= 1<<10 || compressedSize >= 1<<10 {
		sizeFormat, sizeBits, headerSize = 2, 14, 4
		if regeneratedSize >= 1<<14 || compressedSize >= 1<<14 {
			sizeFormat, sizeBits, headerSize = 3, 18, 5
		}
	}
	v := uint64(literalsBlockTypeCompressed) | uint64(sizeFormat)<<2 | uint64(regeneratedSize)<<4 | uint64(compressedSize)<<(4+sizeBits)
	for i := 0; i < headerSize; i++ {
		out = append(out, byte(v>>(8*i)))
	}
	return append(out, streams...), true
}

// appendLiteralsSection appends the smallest literals section that
// is capable of representing the provided literals. On ties, raw is
// preferred over RLE, which is preferred over compressed.
func (es *entropyScratch) appendLiteralsSection(out, literals []byte) []byte {
	rawSize := getRawOrRLELiteralsHeaderSize(len(literals)) + len(literals)
	bestSize := rawSize
	bestType := literalsBlockTypeRaw

	if len(literals) > 0 {
		allEqual := true
		for _, l := range literals[1:] {
			if l != literals[0] {
				allEqual = false
				break
			}
		}
		if allEqual {
			if rleSize := getRawOrRLELiteralsHeaderSize(len(literals)) + 1; rleSize < bestSize {
				bestSize = rleSize
				bestType = literalsBlockTypeRLE
			}
		}
	}

	// Attempt Huffman compression directly in the output buffer,
	// discarding it if it's not smaller.
	if compressed, ok := es.appendHuffmanCompressedLiterals(out, literals); ok && len(compressed)-len(out) < bestSize {
		return compressed
	}

	switch bestType {
	case literalsBlockTypeRLE:
		return append(appendRawOrRLELiteralsHeader(out, literalsBlockTypeRLE, len(literals)), literals[0])
	default:
		return append(appendRawOrRLELiteralsHeader(out, literalsBlockTypeRaw, len(literals)), literals...)
	}
}
