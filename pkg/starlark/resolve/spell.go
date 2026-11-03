package resolve

import (
	"strings"
)

// nearest returns the candidate that is most similar to the provided
// name, or the empty string if none of them are similar enough to be a
// likely misspelling.
func nearest(name string, candidates []string) string {
	// Permit one edit for every two characters, as Bazel does.
	maximumDistance := (len(name) + 1) / 2
	best, bestDistance := "", maximumDistance+1
	lowerName := strings.ToLower(name)
	for _, candidate := range candidates {
		if candidate == name {
			continue
		}
		if d := levenshtein(lowerName, strings.ToLower(candidate)); d < bestDistance || (d == bestDistance && candidate < best) {
			best, bestDistance = candidate, d
		}
	}
	if bestDistance > maximumDistance {
		return ""
	}
	return best
}

// levenshtein returns the edit distance between two strings.
func levenshtein(a, b string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}
