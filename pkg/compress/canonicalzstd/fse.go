package canonicalzstd

import (
	"math/bits"
	"slices"
)

const minimumFSETableLog = 5

// resize a slice to a given length, reusing its storage if
// possible. The contents of the resulting slice are undefined.
func resize[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	return s[:n]
}

// fseScratch holds memory that is reused when building FSE tables.
type fseScratch struct {
	remainders  []uint64
	roundedDown []int
	tableSymbol []uint8
	cumulative  []int
	next        []int
}

// normalizeCounts converts symbol occurrence counts to a normalized
// distribution whose sum equals 1<<tableLog, as specified in section
// "FSE normalization" of README.md. At least two counts
// must be non-zero, and the number of non-zero counts may not exceed
// 1<<tableLog. The result is written into norm, which is returned.
func (fs *fseScratch) normalizeCounts(norm []int16, counts []uint32, tableLog uint) []int16 {
	var total uint64
	for _, c := range counts {
		total += uint64(c)
	}

	tableSize := 1 << tableLog
	norm = resize(norm, len(counts))
	remainders := resize(fs.remainders, len(counts))
	fs.remainders = remainders
	roundedDown := fs.roundedDown[:0]
	sum := 0
	for s, c := range counts {
		norm[s] = 0
		if c > 0 {
			scaled := uint64(c) << tableLog
			if q := scaled / total; q == 0 {
				norm[s] = 1
			} else {
				norm[s] = int16(q)
				remainders[s] = scaled % total
				roundedDown = append(roundedDown, s)
			}
			sum += int(norm[s])
		}
	}
	fs.roundedDown = roundedDown

	if sum < tableSize {
		// Hand out the deficit to the symbols that were
		// rounded down the most.
		slices.SortStableFunc(roundedDown, func(a, b int) int {
			if remainders[a] > remainders[b] {
				return -1
			}
			if remainders[a] < remainders[b] {
				return 1
			}
			return 0
		})
		for _, s := range roundedDown[:tableSize-sum] {
			norm[s]++
		}
	} else {
		// Symbols with a tiny probability were rounded up to
		// one. Compensate by taking away from the symbols
		// with the highest probability.
		for ; sum > tableSize; sum-- {
			largest := 0
			for s, n := range norm {
				if n > norm[largest] {
					largest = s
				}
			}
			norm[largest]--
		}
	}
	return norm
}

// fseTable contains the state needed to encode symbols using an FSE
// table. Its layout is identical to the one used by the reference
// implementation's FSE_buildCTable().
type fseTable struct {
	tableLog       uint
	stateTable     []uint16
	deltaNbBits    []uint32
	deltaFindState []int32
	// Encoder state corresponding to the first position in the
	// decoding table at which a given symbol occurs.
	initialState []uint32
}

func newFSETable(norm []int16, tableLog uint) *fseTable {
	var t fseTable
	var fs fseScratch
	t.build(norm, tableLog, &fs)
	return &t
}

// build the FSE table for a normalized distribution, reusing
// previously allocated memory.
func (t *fseTable) build(norm []int16, tableLog uint, fs *fseScratch) {
	tableSize := 1 << tableLog
	tableMask := tableSize - 1

	// Place symbols with a "less than one" probability at the end
	// of the table.
	cumulative := resize(fs.cumulative, len(norm)+1)
	fs.cumulative = cumulative
	tableSymbol := resize(fs.tableSymbol, tableSize)
	fs.tableSymbol = tableSymbol
	cumulative[0] = 0
	highThreshold := tableSize - 1
	for s, n := range norm {
		if n == -1 {
			cumulative[s+1] = cumulative[s] + 1
			tableSymbol[highThreshold] = uint8(s)
			highThreshold--
		} else {
			cumulative[s+1] = cumulative[s] + int(n)
		}
	}

	// Spread the remaining symbols across the table.
	step := tableSize>>1 + tableSize>>3 + 3
	position := 0
	for s, n := range norm {
		for i := 0; i < int(n); i++ {
			tableSymbol[position] = uint8(s)
			position = (position + step) & tableMask
			for position > highThreshold {
				position = (position + step) & tableMask
			}
		}
	}
	if position != 0 {
		panic("normalized counts do not sum up to table size")
	}

	// Compute the next state for each symbol, sorted by position.
	t.tableLog = tableLog
	t.stateTable = resize(t.stateTable, tableSize)
	t.deltaNbBits = resize(t.deltaNbBits, len(norm))
	t.deltaFindState = resize(t.deltaFindState, len(norm))
	t.initialState = resize(t.initialState, len(norm))
	next := append(fs.next[:0], cumulative...)
	fs.next = next
	for u, s := range tableSymbol {
		t.stateTable[next[s]] = uint16(tableSize + u)
		next[s]++
	}

	total := 0
	for s, n := range norm {
		switch n {
		case 0:
			t.deltaNbBits[s] = uint32(tableLog+1)<<16 - uint32(tableSize)
			t.deltaFindState[s] = 0
			t.initialState[s] = 0
		case -1, 1:
			t.deltaNbBits[s] = uint32(tableLog)<<16 - uint32(tableSize)
			t.deltaFindState[s] = int32(total - 1)
			t.initialState[s] = uint32(t.stateTable[total])
			total++
		default:
			maximumBitsOut := tableLog - uint(bits.Len16(uint16(n-1))-1)
			minimumStatePlus := uint32(n) << maximumBitsOut
			t.deltaNbBits[s] = uint32(maximumBitsOut)<<16 - minimumStatePlus
			t.deltaFindState[s] = int32(total - int(n))
			t.initialState[s] = uint32(t.stateTable[total])
			total += int(n)
		}
	}
}

// encode a single symbol, writing the bits needed by the decoder to
// transition to the state preceding the current one.
func (t *fseTable) encode(w *bitWriter, state *uint32, symbol uint8) {
	nbBitsOut := (*state + t.deltaNbBits[symbol]) >> 16
	w.addBits(uint64(*state), uint(nbBitsOut))
	*state = uint32(t.stateTable[int32(*state>>nbBitsOut)+t.deltaFindState[symbol]])
}

// flush the final state, so that the decoder can use it as its
// initial state.
func (t *fseTable) flush(w *bitWriter, state uint32) {
	w.addBits(uint64(state), t.tableLog)
}

// getEncodedSizeBits computes the number of bits needed to encode a
// sequence of symbols using a single state, including the final
// state.
func (t *fseTable) getEncodedSizeBits(symbols []uint8) int {
	stateTable, deltaNbBits, deltaFindState := t.stateTable, t.deltaNbBits, t.deltaFindState
	state := t.initialState[symbols[len(symbols)-1]]
	sizeBits := uint32(t.tableLog)
	for i := len(symbols) - 2; i >= 0; i-- {
		symbol := symbols[i]
		nbBitsOut := (state + deltaNbBits[symbol]) >> 16
		sizeBits += nbBitsOut
		state = uint32(stateTable[int32(state>>nbBitsOut)+deltaFindState[symbol]])
	}
	return int(sizeBits)
}

// getEncodedSizeBitsBounds computes a lower and an upper bound of the
// value returned by getEncodedSizeBits(), using only the number of
// occurrences of each symbol. The number of bits emitted for a symbol
// only depends on the state, which is in [2^tableLog, 2^(tableLog+1)).
// The last symbol is excluded, as it is encoded by the final state.
func (t *fseTable) getEncodedSizeBitsBounds(counts []uint32, lastSymbol uint8) (int, int) {
	lowestState := uint32(1) << t.tableLog
	highestState := 2*lowestState - 1
	lowerBound := int(t.tableLog)
	upperBound := int(t.tableLog)
	for symbol, count := range counts {
		if count > 0 {
			if symbol == int(lastSymbol) {
				count--
			}
			d := t.deltaNbBits[symbol]
			lowerBound += int(count * ((lowestState + d) >> 16))
			upperBound += int(count * ((highestState + d) >> 16))
		}
	}
	return lowerBound, upperBound
}

// getEncodedSizeBitsPair is equivalent to calling
// getEncodedSizeBits() on two tables. It is faster, as the
// computations for both tables are independent and can be
// interleaved.
func getEncodedSizeBitsPair(t1, t2 *fseTable, symbols []uint8) (int, int) {
	stateTable1, deltaNbBits1, deltaFindState1 := t1.stateTable, t1.deltaNbBits, t1.deltaFindState
	stateTable2, deltaNbBits2, deltaFindState2 := t2.stateTable, t2.deltaNbBits, t2.deltaFindState
	lastSymbol := symbols[len(symbols)-1]
	state1, state2 := t1.initialState[lastSymbol], t2.initialState[lastSymbol]
	sizeBits1, sizeBits2 := uint32(t1.tableLog), uint32(t2.tableLog)
	for i := len(symbols) - 2; i >= 0; i-- {
		symbol := symbols[i]
		nbBitsOut1 := (state1 + deltaNbBits1[symbol]) >> 16
		nbBitsOut2 := (state2 + deltaNbBits2[symbol]) >> 16
		sizeBits1 += nbBitsOut1
		sizeBits2 += nbBitsOut2
		state1 = uint32(stateTable1[int32(state1>>nbBitsOut1)+deltaFindState1[symbol]])
		state2 = uint32(stateTable2[int32(state2>>nbBitsOut2)+deltaFindState2[symbol]])
	}
	return int(sizeBits1), int(sizeBits2)
}

// appendFSETableDescription appends the description of a normalized
// distribution, using the format described in RFC 8878, section
// 4.1.1. The bit layout is identical to the one generated by the
// reference implementation's FSE_writeNCount().
func appendFSETableDescription(out []byte, norm []int16, tableLog uint) []byte {
	w := bitWriter{out: out}
	w.addBits(uint64(tableLog-minimumFSETableLog), 4)

	remaining := 1<<tableLog + 1
	threshold := 1 << tableLog
	nbBits := tableLog + 1
	previousIsZero := false
	for symbol := 0; symbol < len(norm) && remaining > 1; {
		if previousIsZero {
			start := symbol
			for norm[symbol] == 0 {
				symbol++
			}
			for symbol >= start+3 {
				start += 3
				w.addBits(3, 2)
			}
			w.addBits(uint64(symbol-start), 2)
		}

		count := int(norm[symbol])
		symbol++
		maximum := 2*threshold - 1 - remaining
		if count < 0 {
			remaining += count
		} else {
			remaining -= count
		}
		count++
		if count >= threshold {
			count += maximum
		}
		if count < maximum {
			w.addBits(uint64(count), nbBits-1)
		} else {
			w.addBits(uint64(count), nbBits)
		}
		previousIsZero = count == 1
		for remaining < threshold {
			nbBits--
			threshold >>= 1
		}
	}
	if remaining != 1 {
		panic("normalized counts do not sum up to table size")
	}
	return w.flush()
}

// ceilLog2 returns the smallest integer l such that 1<<l >= n.
func ceilLog2(n int) uint {
	return uint(bits.Len(uint(n - 1)))
}
