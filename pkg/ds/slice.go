package ds

// Slice is a wrapper around a Go's built-in slice type. It provides a
// couple of helper functions that make it easier to satisfy interfaces
// such as sort.Interface and heap.Interface.
type Slice[T any] []T

// Len returns the length of the slice.
func (s Slice[T]) Len() int {
	return len(s)
}

// Swap two elements contained in the slice.
func (s Slice[T]) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

// Push a new element to the tail of the slice.
func (s *Slice[T]) Push(x any) {
	*s = append(*s, x.(T))
}

// Pop an element from the tail of the slice. The element in the slice
// is set to zero, so that any objects that were references by it may be
// garbage collected.
func (s *Slice[T]) Pop() any {
	last := (*s)[len(*s)-1]
	var defaultValue T
	(*s)[len(*s)-1] = defaultValue
	*s = (*s)[:len(*s)-1]
	return last
}
