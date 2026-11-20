package search

import (
	"fmt"
	"io"
)

// MultiSearchAndReplacer is capable of searching for multiple strings
// in a stream of data and replacing it.
//
// The algorithm that is used to perform string matching is based on the
// algorithm described in "Efficient String Matching: An Aid to
// Bibliographic Search" by Aho and Corasick:
//
// https://doi.org/10.1145%2F360825.360855
//
// Noteworthy differences between the algorithm as described in the
// paper and this implementation include:
//
//   - The goto function g(state, a) is implemented as a hash table,
//     meaning memory usage is not proportional to the alphabet.
//
//   - Instead of an output function that returns a set of needles
//     matching at a given location, every node in the goto graph holds
//     the part of the needle matching up to that point. This makes it
//     possible to emit the needle upon mismatch.
type MultiSearchAndReplacer struct {
	needles [][]byte
	gTable  []uint64
	gMask   uint64
	nodes   []gotoGraphNode
}

// gotoGraphNode contains state that is associated with a single node in
// the goto graph.
type gotoGraphNode struct {
	// The needle that is partially matched within the current
	// state, and the length of the partial match.
	needleIndex  uint32
	needleLength uint32

	// The failure function, i.e., the state in which to continue if
	// a mismatch occurs.
	f uint32
}

const maximumStateBits = (64 - 8) / 2

// NewMultiSearchAndReplacer creates a MultiSearchAndReplacer that is
// capable of matching the provided set of needles. An error is returned
// if one or more needles are empty or contained inside each other.
func NewMultiSearchAndReplacer(needles [][]byte) (*MultiSearchAndReplacer, error) {
	// Hash table used for the goto function. The encoding of hash
	// table entries is as follows:
	//
	// - Top 28 bits: Code of the current state.
	// - Middle 8 bits: Next symbol.
	// - Bottom 28 bits: Code of the next prefix.
	//
	// A single hash table is shared by all states, as most states
	// only have a very small number of outgoing edges. Entries for
	// g(0, a) == 0 are not stored explicitly.
	gTable := make([]uint64, 0x100)
	gMask := uint64(0xff)

	// For each state, the list of symbols for which the goto
	// function is defined. This is needed by algorithm 3. It is
	// discarded after preprocessing completes.
	gSetByState := [][]byte{nil}

	// The output function, indicating whether a state corresponds
	// to the end of a needle.
	nodes := []gotoGraphNode{{}}

	// Algorithm 2: Construction of the goto function.
	for needleIndex, a := range needles {
		if len(a) == 0 {
			return nil, fmt.Errorf("needle at index %d is empty", needleIndex)
		}

		// Walk the prefix that already exists in the goto graph.
		state := uint32(0)
		j := 0
	WalkExistingStates:
		for ; j < len(a); j++ {
			gStateAjKey := uint64(state)<<8 | uint64(a[j])
			gStateAjHash := gStateAjKey>>8 ^ gStateAjKey
			for h, i := gStateAjHash, uint64(1); ; h, i = h+i, i+1 {
				gStateAj := gTable[h&gMask]
				if gStateAj == 0 {
					break WalkExistingStates
				}
				if gStateAj>>maximumStateBits == gStateAjKey {
					state = uint32(gStateAj & (1<<maximumStateBits - 1))
					break
				}
			}
		}

		existingNeedleIndex := nodes[state].needleIndex
		if len(needles[existingNeedleIndex]) == j {
			return nil, fmt.Errorf("needle at index %d is a prefix of needle at index %d", existingNeedleIndex, needleIndex)
		}
		newStates := len(a) - j
		if newStates == 0 {
			return nil, fmt.Errorf("needle at index %d is a prefix of needle at index %d", needleIndex, existingNeedleIndex)
		}

		// Grow the goto function's hash table to fit the edges
		// to the states of the newly observed suffix.
		if minimumGSize := (len(nodes) + newStates) * 4; len(gTable) < minimumGSize {
			newGSize := len(gTable)
			for newGSize < minimumGSize {
				newGSize *= 2
			}
			newGTable := make([]uint64, newGSize)
			newGMask := uint64(len(newGTable) - 1)
			for _, g := range gTable {
				gKey := g >> maximumStateBits
				gHash := gKey>>8 ^ gKey
				for h, i := gHash, uint64(1); ; h, i = h+i, i+1 {
					if newG := &newGTable[h&newGMask]; *newG == 0 {
						*newG = g
						break
					}
				}
			}
			gTable = newGTable
			gMask = newGMask
		}

		// Create states of the newly observed suffix.
		for p := j; p < len(a); p++ {
			gSetByState[state] = append(gSetByState[state], a[p])
			gStateApKey := uint64(state)<<8 | uint64(a[p])
			gStateApHash := gStateApKey>>8 ^ gStateApKey
			for h, i := gStateApHash, uint64(1); ; h, i = h+i, i+1 {
				if gStateAp := &gTable[h&gMask]; *gStateAp == 0 {
					state = uint32(len(nodes))
					if state >= 1<<maximumStateBits {
						return nil, fmt.Errorf("goto graph requires more than %d states", 1<<maximumStateBits)
					}
					nodes = append(nodes, gotoGraphNode{
						needleIndex:  uint32(needleIndex),
						needleLength: uint32(p) + 1,
					})
					gSetByState = append(gSetByState, nil)
					*gStateAp = gStateApKey<<maximumStateBits | uint64(state)
					break
				}
			}
		}
	}

	// Algorithm 3: construction of the failure function.
	var queue []uint32
	for _, a := range gSetByState[0] {
		for h, i := uint64(a), uint64(1); ; h, i = h+i, i+1 {
			if g0A := gTable[h&gMask]; g0A>>maximumStateBits == uint64(a) {
				s := uint32(g0A & (1<<maximumStateBits - 1))
				queue = append(queue, s)
				break
			}
		}
	}
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
	ProcessSymbol:
		for _, a := range gSetByState[r] {
			gRAKey := uint64(r)<<8 | uint64(a)
			for h, i := gRAKey>>8^gRAKey, uint64(1); ; h, i = h+i, i+1 {
				gRA := gTable[h&gMask]
				if gRA == 0 {
					break
				}
				if gRA>>maximumStateBits == gRAKey {
					s := uint32(gRA & (1<<maximumStateBits - 1))
					queue = append(queue, s)
					state := nodes[r].f
					for {
						gStateAKey := uint64(state)<<8 | uint64(a)
						for h, i := gStateAKey>>8^gStateAKey, uint64(1); ; h, i = h+i, i+1 {
							gStateA := gTable[h&gMask]
							if gStateA>>maximumStateBits == gStateAKey || (gStateA == 0 && state == 0) {
								nodes[s].f = uint32(gStateA & (1<<maximumStateBits - 1))
								if node := nodes[nodes[s].f]; len(needles[node.needleIndex]) == int(node.needleLength) && node.needleIndex != nodes[s].needleIndex {
									return nil, fmt.Errorf("needle at index %d is contained in needle at index %d", node.needleIndex, nodes[s].needleIndex)
								}
								continue ProcessSymbol
							}
							if gStateA == 0 {
								state = nodes[state].f
								break
							}
						}
					}
				}
			}
		}
	}

	return &MultiSearchAndReplacer{
		needles: needles,
		gTable:  gTable,
		gMask:   gMask,
		nodes:   nodes,
	}, nil
}

// SearchAndReplace reads data from a source and writes it to a sink
// with matching occurrences of the needles substituted by replacement
// strings.
func (s *MultiSearchAndReplacer) SearchAndReplace(dst io.Writer, src io.ByteReader, replacements [][]byte) error {
	state := uint32(0)
ProcessCharacter:
	for {
		a, err := src.ReadByte()
		if err != nil {
			if err == io.EOF {
				if state != 0 {
					// Partial match at the end of the
					// file. Write the part of the
					// needle that was already matched.
					node := s.nodes[state]
					if _, err := dst.Write(s.needles[node.needleIndex][:node.needleLength]); err != nil {
						return err
					}
				}
				return nil
			}
			return err
		}

		for {
			gStateAKey := uint64(state)<<8 | uint64(a)
			for h, i := gStateAKey>>8^gStateAKey, uint64(1); ; h, i = h+i, i+1 {
				gStateA := s.gTable[h&s.gMask]
				if gStateA>>maximumStateBits == gStateAKey {
					// Symbol matches with the next
					// character of the needles that
					// were already partially matched.
					state = uint32(gStateA & (1<<maximumStateBits - 1))
					if node := s.nodes[state]; len(s.needles[node.needleIndex]) == int(node.needleLength) {
						// Full match on the needle. Write the
						// replacement and go to the initial state.
						if _, err := dst.Write(replacements[node.needleIndex]); err != nil {
							return err
						}
						state = 0
					}
					continue ProcessCharacter
				}
				if gStateA == 0 {
					if state == 0 {
						// Mismatch while in the
						// initial state. Echo the
						// input and continue to the
						// next character.
						if _, err := dst.Write([]byte{a}); err != nil {
							return err
						}
						continue ProcessCharacter
					}

					// Mismatch while already having a
					// partial match with a needle.
					//
					// Restore the original input by
					// writing the part of the needle that
					// will no longer be considered for
					// replacement.
					//
					// Use the fail function to determine
					// the next state and retry matching.
					oldNode := s.nodes[state]
					state = oldNode.f
					newNode := s.nodes[state]
					if _, err := dst.Write(s.needles[oldNode.needleIndex][:oldNode.needleLength-newNode.needleLength]); err != nil {
						return err
					}
					break
				}
			}
		}
	}
}
