package canonicalzstd

import (
	"encoding/binary"
	"math/bits"
)

// sequence of literals followed by a match, as described in RFC
// 8878, section 3.1.1.3.2.
type sequence struct {
	literalLength uint32
	matchLength   uint32
	offsetValue   uint32
}

const (
	symbolCompressionModePredefined = 0
	symbolCompressionModeRLE        = 1
	symbolCompressionModeFSE        = 2
)

var (
	literalLengthBaselines = [...]uint32{
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
		16, 18, 20, 22, 24, 28, 32, 40, 48, 64, 128, 256, 512, 1024, 2048, 4096,
		8192, 16384, 32768, 65536,
	}
	literalLengthExtraBits = [...]uint8{
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		1, 1, 1, 1, 2, 2, 3, 3, 4, 6, 7, 8, 9, 10, 11, 12,
		13, 14, 15, 16,
	}
	matchLengthBaselines = [...]uint32{
		3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18,
		19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34,
		35, 37, 39, 41, 43, 47, 51, 59, 67, 83, 99, 131, 259, 515, 1027, 2051,
		4099, 8195, 16387, 32771, 65539,
	}
	matchLengthExtraBits = [...]uint8{
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		1, 1, 1, 1, 2, 2, 3, 3, 4, 4, 5, 7, 8, 9, 10, 11,
		12, 13, 14, 15, 16,
	}
)

// symbolStream describes the properties of one of the three FSE
// encoded streams of symbols: literal lengths, offsets and match
// lengths.
type symbolStream struct {
	maximumSymbol   int
	maximumTableLog uint
	predefined      *fseTable
	// Number of symbols for which the predefined distribution
	// provides a non-zero probability.
	predefinedSymbols int
}

var (
	literalLengthStream = newSymbolStream(35, 9, 6, []int16{
		4, 3, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1,
		2, 2, 2, 2, 2, 2, 2, 2, 2, 3, 2, 1, 1, 1, 1, 1,
		-1, -1, -1, -1,
	})
	matchLengthStream = newSymbolStream(52, 9, 6, []int16{
		1, 4, 3, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, -1, -1,
		-1, -1, -1, -1, -1,
	})
	offsetStream = newSymbolStream(31, 8, 5, []int16{
		1, 1, 1, 1, 1, 1, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, -1, -1, -1, -1, -1,
	})
)

func newSymbolStream(maximumSymbol int, maximumTableLog, predefinedTableLog uint, predefinedNorm []int16) symbolStream {
	return symbolStream{
		maximumSymbol:     maximumSymbol,
		maximumTableLog:   maximumTableLog,
		predefined:        newFSETable(predefinedNorm, predefinedTableLog),
		predefinedSymbols: len(predefinedNorm),
	}
}

// encodedSymbolStream contains the encoding parameters for a
// stream of symbols that were chosen by chooseEncoding().
type encodedSymbolStream struct {
	mode        int
	description []byte
	// Table to use for encoding. Nil for RLE.
	table *fseTable
}

// rleFSETable is an FSE table that emits no bits and remains in state
// zero. It is used to encode streams in RLE mode.
var rleFSETable = fseTable{
	stateTable:     []uint16{0},
	deltaNbBits:    make([]uint32, 256),
	deltaFindState: make([]int32, 256),
	initialState:   make([]uint32, 256),
}

// symbolStreamScratch holds memory that is reused when choosing the
// encoding of a stream of symbols.
type symbolStreamScratch struct {
	counts      []uint32
	norm        []int16
	description []byte
	table       fseTable
	rle         [1]byte
}

// getFSETableLog returns the accuracy log to use when encoding a
// stream of symbols using FSE_Compressed_Mode.
func (ss *symbolStream) getFSETableLog(symbolCount, distinctSymbols int) uint {
	tableLog := int(ceilLog2(symbolCount)) - 2
	tableLog = min(tableLog, int(ss.maximumTableLog))
	tableLog = max(tableLog, minimumFSETableLog, int(ceilLog2(distinctSymbols)))
	return uint(tableLog)
}

// chooseEncoding selects the encoding for a stream of symbols that
// minimizes the size of the table description in bits, plus the
// number of bits emitted by the FSE state. On ties, predefined mode
// is preferred, followed by RLE and FSE compressed mode.
func (ss *symbolStream) chooseEncoding(symbols []uint8, sc *symbolStreamScratch, fs *fseScratch) encodedSymbolStream {
	counts := resize(sc.counts, ss.maximumSymbol+1)
	sc.counts = counts
	clear(counts)
	distinctSymbols := 0
	largestSymbol := 0
	for _, s := range symbols {
		if counts[s] == 0 {
			distinctSymbols++
		}
		counts[s]++
		largestSymbol = max(largestSymbol, int(s))
	}

	predefinedAllowed := largestSymbol < ss.predefinedSymbols
	predefined := encodedSymbolStream{
		mode:  symbolCompressionModePredefined,
		table: ss.predefined,
	}
	if distinctSymbols == 1 {
		// RLE mode always costs 8 bits.
		if predefinedAllowed && ss.predefined.getEncodedSizeBits(symbols) <= 8 {
			return predefined
		}
		sc.rle[0] = symbols[0]
		return encodedSymbolStream{
			mode:        symbolCompressionModeRLE,
			description: sc.rle[:],
		}
	}

	tableLog := ss.getFSETableLog(len(symbols), distinctSymbols)
	sc.norm = fs.normalizeCounts(sc.norm, counts[:largestSymbol+1], tableLog)
	sc.description = appendFSETableDescription(sc.description[:0], sc.norm, tableLog)
	sc.table.build(sc.norm, tableLog, fs)
	best := encodedSymbolStream{
		mode:        symbolCompressionModeFSE,
		description: sc.description,
		table:       &sc.table,
	}
	if predefinedAllowed {
		// Computing the exact cost requires simulating the
		// encoding process. Only do this if bounds on the cost
		// that can be derived from the symbol counts are
		// inconclusive.
		descriptionBits := 8 * len(sc.description)
		lastSymbol := symbols[len(symbols)-1]
		predefinedLowerBound, predefinedUpperBound := ss.predefined.getEncodedSizeBitsBounds(counts[:largestSymbol+1], lastSymbol)
		fseLowerBound, fseUpperBound := sc.table.getEncodedSizeBitsBounds(counts[:largestSymbol+1], lastSymbol)
		if predefinedUpperBound <= descriptionBits+fseLowerBound {
			return predefined
		}
		if predefinedLowerBound > descriptionBits+fseUpperBound {
			return best
		}
		predefinedCostBits, fseCostBits := getEncodedSizeBitsPair(ss.predefined, &sc.table, symbols)
		if predefinedCostBits <= descriptionBits+fseCostBits {
			return predefined
		}
	}
	return best
}

func getLiteralLengthCode(literalLength uint32) uint8 {
	if literalLength < 16 {
		return uint8(literalLength)
	}
	code := uint8(16)
	for literalLengthBaselines[code+1] <= literalLength {
		code++
		if int(code) == len(literalLengthBaselines)-1 {
			break
		}
	}
	return code
}

func getMatchLengthCode(matchLength uint32) uint8 {
	if matchLength < 35 {
		return uint8(matchLength - 3)
	}
	code := uint8(32)
	for matchLengthBaselines[code+1] <= matchLength {
		code++
		if int(code) == len(matchLengthBaselines)-1 {
			break
		}
	}
	return code
}

// entropyScratch holds memory that is reused when entropy coding
// blocks.
type entropyScratch struct {
	fseScratch
	huffmanScratch

	literalLengthCodes []uint8
	offsetCodes        []uint8
	matchLengthCodes   []uint8
	literalLengths     symbolStreamScratch
	offsets            symbolStreamScratch
	matchLengths       symbolStreamScratch
}

// appendSequencesSection appends a sequences section, as described in
// RFC 8878, section 3.1.1.3.2.
func (es *entropyScratch) appendSequencesSection(out []byte, sequences []sequence) []byte {
	n := len(sequences)
	switch {
	case n < 128:
		out = append(out, byte(n))
	case n < 0x7f00:
		out = append(out, byte(n>>8+128), byte(n))
	default:
		out = append(out, 255, byte(n-0x7f00), byte((n-0x7f00)>>8))
	}
	if n == 0 {
		return out
	}

	literalLengthCodes := resize(es.literalLengthCodes, n)
	es.literalLengthCodes = literalLengthCodes
	offsetCodes := resize(es.offsetCodes, n)
	es.offsetCodes = offsetCodes
	matchLengthCodes := resize(es.matchLengthCodes, n)
	es.matchLengthCodes = matchLengthCodes
	for i, s := range sequences {
		literalLengthCodes[i] = getLiteralLengthCode(s.literalLength)
		offsetCodes[i] = uint8(bits.Len32(s.offsetValue) - 1)
		matchLengthCodes[i] = getMatchLengthCode(s.matchLength)
	}

	literalLengths := literalLengthStream.chooseEncoding(literalLengthCodes, &es.literalLengths, &es.fseScratch)
	offsets := offsetStream.chooseEncoding(offsetCodes, &es.offsets, &es.fseScratch)
	matchLengths := matchLengthStream.chooseEncoding(matchLengthCodes, &es.matchLengths, &es.fseScratch)
	out = append(out, byte(literalLengths.mode<<6|offsets.mode<<4|matchLengths.mode<<2))
	out = append(out, literalLengths.description...)
	out = append(out, offsets.description...)
	out = append(out, matchLengths.description...)

	// Encode the sequences in reverse order. RLE mode is
	// implemented using a table that emits no bits.
	llTable, ofTable, mlTable := literalLengths.table, offsets.table, matchLengths.table
	if llTable == nil {
		llTable = &rleFSETable
	}
	if ofTable == nil {
		ofTable = &rleFSETable
	}
	if mlTable == nil {
		mlTable = &rleFSETable
	}
	last := n - 1
	llState := llTable.initialState[literalLengthCodes[last]]
	ofState := ofTable.initialState[offsetCodes[last]]
	mlState := mlTable.initialState[matchLengthCodes[last]]

	// Bits are accumulated in a local variable, which is flushed
	// whenever it holds at least 32 bits. At most 31 bits are
	// pending at the start of each step, and each step adds at
	// most 32 bits.
	var acc uint64
	var nBits uint
	for i := last; i >= 0; i-- {
		llCode, ofCode, mlCode := literalLengthCodes[i], offsetCodes[i], matchLengthCodes[i]
		if i != last {
			// Offset, match length and literal length
			// states use at most 9 bits each.
			nb := (ofState + ofTable.deltaNbBits[ofCode]) >> 16
			acc |= uint64(ofState&(1<<nb-1)) << nBits
			nBits += uint(nb)
			ofState = uint32(ofTable.stateTable[int32(ofState>>nb)+ofTable.deltaFindState[ofCode]])

			nb = (mlState + mlTable.deltaNbBits[mlCode]) >> 16
			acc |= uint64(mlState&(1<<nb-1)) << nBits
			nBits += uint(nb)
			mlState = uint32(mlTable.stateTable[int32(mlState>>nb)+mlTable.deltaFindState[mlCode]])

			nb = (llState + llTable.deltaNbBits[llCode]) >> 16
			acc |= uint64(llState&(1<<nb-1)) << nBits
			nBits += uint(nb)
			llState = uint32(llTable.stateTable[int32(llState>>nb)+llTable.deltaFindState[llCode]])

			if nBits >= 32 {
				out = binary.LittleEndian.AppendUint32(out, uint32(acc))
				acc >>= 32
				nBits -= 32
			}
		}

		// Literal length and match length extra bits use at
		// most 16 bits each.
		s := &sequences[i]
		acc |= uint64(s.literalLength-literalLengthBaselines[llCode]) << nBits
		nBits += uint(literalLengthExtraBits[llCode])
		acc |= uint64(s.matchLength-matchLengthBaselines[mlCode]) << nBits
		nBits += uint(matchLengthExtraBits[mlCode])
		if nBits >= 32 {
			out = binary.LittleEndian.AppendUint32(out, uint32(acc))
			acc >>= 32
			nBits -= 32
		}

		// Offset extra bits use at most 31 bits.
		acc |= uint64(s.offsetValue&(1<<ofCode-1)) << nBits
		nBits += uint(ofCode)
		if nBits >= 32 {
			out = binary.LittleEndian.AppendUint32(out, uint32(acc))
			acc >>= 32
			nBits -= 32
		}
	}
	w := bitWriter{out: out, acc: acc, nBits: nBits}
	mlTable.flush(&w, mlState)
	ofTable.flush(&w, ofState)
	llTable.flush(&w, llState)
	return w.close()
}
