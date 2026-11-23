package canonicalzstd

import (
	"encoding/binary"
	"math/bits"
)

const (
	minimumMatchLength = 4

	// Matches are located using a hash table. Each bucket holds
	// the most recent positions whose four byte prefix hashes to
	// that bucket.
	hashTableMinimumLog = 8
	hashTableMaximumLog = 16
	hashTableWays       = 2
	hashMultiplier      = 0x9e3779b1

	// When no match is found, the parser advances by one position,
	// plus one position for every 1<<literalSkipShift literals
	// emitted since the last match, up to maximumLiteralSkip
	// additional positions. This speeds up compression of
	// incompressible data. The limit prevents the parser from
	// skipping over the start of compressible data after a long
	// run of literals, as skipped positions are not inserted into
	// the hash table.
	literalSkipShift   = 4
	maximumLiteralSkip = 31
)

// match is a candidate match at a given position.
type match struct {
	length      uint32
	offsetValue uint32
}

func (m match) score() int {
	return 4*int(m.length) - bits.Len32(m.offsetValue)
}

// isBetterThan returns true if match m is preferred over match o
// when both start at the same position.
func (m match) isBetterThan(o match) bool {
	if sm, so := m.score(), o.score(); sm != so {
		return sm > so
	}
	if m.length != o.length {
		return m.length > o.length
	}
	return m.offsetValue < o.offsetValue
}

// repeatedOffsets holds the three most recently used offsets.
type repeatedOffsets [3]uint32

var initialRepeatedOffsets = repeatedOffsets{1, 4, 8}

// getOffsetValue returns the smallest Offset_Value that encodes a
// given offset.
func (r *repeatedOffsets) getOffsetValue(offset, literalLength uint32) uint32 {
	if literalLength > 0 {
		for i, ro := range r {
			if offset == ro {
				return uint32(i + 1)
			}
		}
	} else {
		if offset == r[1] {
			return 1
		}
		if offset == r[2] {
			return 2
		}
		if offset == r[0]-1 {
			return 3
		}
	}
	return offset + 3
}

// update the repeated offsets after emitting a sequence, as described
// in RFC 8878, section 3.1.2.5.
func (r *repeatedOffsets) update(offsetValue, literalLength uint32) {
	if offsetValue > 3 {
		*r = repeatedOffsets{offsetValue - 3, r[0], r[1]}
		return
	}
	index := offsetValue
	if literalLength == 0 {
		index++
	}
	switch index {
	case 1:
	case 2:
		*r = repeatedOffsets{r[1], r[0], r[2]}
	case 3:
		*r = repeatedOffsets{r[2], r[0], r[1]}
	case 4:
		*r = repeatedOffsets{r[0] - 1, r[0], r[1]}
	}
}

// matchFinder locates matches within the input.
type matchFinder struct {
	data       []byte
	tableShift uint32
	// Hash table containing positions plus one. Zero denotes an
	// empty slot. Within each bucket, more recent positions come
	// first.
	table []uint32
}

// reset the match finder to process new data, reusing previously
// allocated memory.
func (mf *matchFinder) reset(data []byte) {
	mf.data = data
	if len(data) < minimumMatchLength {
		return
	}
	tableLog := min(max(int(ceilLog2(len(data)))-1, hashTableMinimumLog), hashTableMaximumLog)
	mf.tableShift = uint32(32 - tableLog)
	mf.table = resize(mf.table, hashTableWays<<tableLog)
	clear(mf.table)
}

// getBucket returns the bucket of the hash table that corresponds to
// the four bytes starting at position p.
func (mf *matchFinder) getBucket(prefix uint32) *[hashTableWays]uint32 {
	h := (prefix * hashMultiplier) >> mf.tableShift
	return (*[hashTableWays]uint32)(mf.table[h*hashTableWays:])
}

// insert a position into the hash table, if at least four bytes of
// data are available starting at that position.
func (mf *matchFinder) insert(p uint32) {
	if int(p)+minimumMatchLength <= len(mf.data) {
		b := mf.getBucket(binary.LittleEndian.Uint32(mf.data[p:]))
		copy(b[1:], b[:hashTableWays-1])
		b[0] = p + 1
	}
}

// getMatchLength returns the length of the match between the data at
// position p and p-offset, not extending beyond the end position. The
// first start bytes are assumed to be identical.
func (mf *matchFinder) getMatchLength(p, offset, end, start uint32) uint32 {
	data := mf.data[:end]
	a, b := data[p:], data[p-offset:]
	l := start
	n := end - p
	for l+8 <= n {
		if x := binary.LittleEndian.Uint64(a[l:]) ^ binary.LittleEndian.Uint64(b[l:]); x != 0 {
			return l + uint32(bits.TrailingZeros64(x)/8)
		}
		l += 8
	}
	for l < n && a[l] == b[l] {
		l++
	}
	return l
}

// bestMatchSearch holds the state of the search for the best match at
// a given position.
type bestMatchSearch struct {
	p, end    uint32
	prefix    uint32
	best      match
	bestScore int
}

// consider an offset as a candidate, with a given offset value. The
// caller must ensure that the offset lies within the data, and that
// the four bytes at that offset match the prefix at the current
// position. These checks are performed by the caller, as most
// candidates are rejected by them.
func (mf *matchFinder) consider(s *bestMatchSearch, offset, offsetValue uint32) {
	p := s.p
	data := mf.data

	// Determine the minimum length a match with this offset
	// needs to have to not lose against the best match found so
	// far. Quickly discard the candidate if it can't reach that
	// length. This doesn't affect the outcome.
	requiredLength := uint32(minimumMatchLength)
	if s.best.length > 0 {
		requiredLength = max(requiredLength, uint32((s.bestScore+bits.Len32(offsetValue)+3)/4))
		if requiredLength > s.end-p || data[p+requiredLength-1] != data[p+requiredLength-1-offset] {
			return
		}
	}

	length := mf.getMatchLength(p, offset, s.end, minimumMatchLength)
	if length < requiredLength {
		return
	}
	m := match{length: length, offsetValue: offsetValue}
	if s.best.length == 0 || m.isBetterThan(s.best) {
		s.best = m
		s.bestScore = m.score()
	}
}

// parseBlock converts the contents of a block to a series of
// sequences and literals, which are appended to the provided slices.
func (mf *matchFinder) parseBlock(start, end uint32, rep *repeatedOffsets, sequences []sequence, literals []byte) ([]sequence, []byte) {
	data := mf.data
	literalsStart := start
	p := start
	for p+minimumMatchLength <= end {
		literalLength := p - literalsStart
		s := bestMatchSearch{
			p:      p,
			end:    end,
			prefix: binary.LittleEndian.Uint32(data[p:]),
		}

		// Consider the repeated offset that can be encoded
		// using the smallest offset value.
		repeatedOffset := rep[1]
		if literalLength > 0 {
			repeatedOffset = rep[0]
		}
		if repeatedOffset <= p && binary.LittleEndian.Uint32(data[p-repeatedOffset:]) == s.prefix {
			mf.consider(&s, repeatedOffset, 1)
		}

		// Consider the positions in the hash table bucket, and
		// insert the current position.
		bucket := mf.getBucket(s.prefix)
		for _, q := range bucket {
			// Slots are filled in order, so the first empty
			// slot terminates the bucket.
			if q == 0 {
				break
			}
			if binary.LittleEndian.Uint32(data[q-1:]) == s.prefix {
				offset := p - (q - 1)
				mf.consider(&s, offset, rep.getOffsetValue(offset, literalLength))
			}
		}
		copy(bucket[1:], bucket[:hashTableWays-1])
		bucket[0] = p + 1

		m := s.best
		if m.length == 0 {
			p += 1 + min(literalLength>>literalSkipShift, maximumLiteralSkip)
			continue
		}

		sequences = append(sequences, sequence{
			literalLength: literalLength,
			matchLength:   m.length,
			offsetValue:   m.offsetValue,
		})
		literals = append(literals, data[literalsStart:p]...)
		rep.update(m.offsetValue, literalLength)

		// Insert two positions inside the match, so that future
		// searches can find it.
		matchEnd := p + m.length
		mf.insert(p + 2)
		if matchEnd-2 > p+2 {
			mf.insert(matchEnd - 2)
		}
		p = matchEnd
		literalsStart = p
	}
	literals = append(literals, data[literalsStart:end]...)
	return sequences, literals
}
