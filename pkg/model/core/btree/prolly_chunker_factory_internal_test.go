package btree

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"
)

// getLexicographicMaximumNaive provides the same behavior as
// getLexicographicMaximum(), but compares all sequences fully.
func getLexicographicMaximumNaive(hashes []uint64, cumulativeSizesBytes []int, windowSizeBytes, first, last int) int {
	best := first
	for i := first + 1; i <= last; i++ {
		for k := 0; k == 0 || cumulativeSizesBytes[i+k]-cumulativeSizesBytes[i] < windowSizeBytes; k++ {
			if h, hBest := hashes[i+k], hashes[best+k]; h > hBest {
				best = i
				break
			} else if h < hBest {
				break
			}
		}
	}
	return best
}

func TestGetLexicographicMaximum(t *testing.T) {
	// Compare against the naïve implementation using inputs with
	// a small alphabet, so that lots of repetition occurs.
	random := rand.New(rand.NewPCG(1, 2))
	for iteration := 0; iteration < 200000; iteration++ {
		alphabetSize := 1 + random.IntN(4)
		alphabetSizesBytes := make([]int, alphabetSize)
		for i := range alphabetSizesBytes {
			alphabetSizesBytes[i] = 1 + random.IntN(4)
		}

		var input []int
		if random.IntN(2) == 0 {
			// Random symbols.
			input = make([]int, 1+random.IntN(100))
			for i := range input {
				input[i] = random.IntN(alphabetSize)
			}
		} else {
			// Repetitions of a pattern, followed by
			// random symbols.
			pattern := make([]int, 1+random.IntN(5))
			for i := range pattern {
				pattern[i] = random.IntN(alphabetSize)
			}
			for range 1 + random.IntN(30) {
				input = append(input, pattern...)
			}
			for range random.IntN(10) {
				input = append(input, random.IntN(alphabetSize))
			}
		}

		hashes := make([]uint64, 0, len(input))
		cumulativeSizesBytes := make([]int, 1, len(input)+1)
		for _, symbol := range input {
			hashes = append(hashes, uint64(symbol))
			cumulativeSizesBytes = append(cumulativeSizesBytes, cumulativeSizesBytes[len(cumulativeSizesBytes)-1]+alphabetSizesBytes[symbol])
		}

		// Pick a range of indices for which the sequences are
		// fully contained in the input.
		windowSizeBytes := random.IntN(20)
		last := len(input) - 1
		for last >= 0 && cumulativeSizesBytes[len(input)]-cumulativeSizesBytes[last] < windowSizeBytes+4 {
			last--
		}
		if last < 0 {
			continue
		}
		first := random.IntN(last + 1)
		last = first + random.IntN(last-first+1)

		require.Equal(
			t,
			getLexicographicMaximumNaive(hashes, cumulativeSizesBytes, windowSizeBytes, first, last),
			getLexicographicMaximum(hashes, cumulativeSizesBytes, windowSizeBytes, first, last),
			fmt.Sprintf("input=%v sizes=%v window=%d first=%d last=%d", input, alphabetSizesBytes, windowSizeBytes, first, last),
		)
	}
}
