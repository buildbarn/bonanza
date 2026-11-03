package eval

import (
	"iter"

	"bonanza.build/pkg/starlark/syntax"
)

// Dict is implemented by all Starlark dicts, regardless of whether they
// reside in memory or in storage. Iterating over a dict yields its
// keys in insertion order.
type Dict interface {
	Sequence
	Mapping
	isDict()
	// Items returns the entries of the dict in insertion order.
	Items(t *Thread, errOut *error) iter.Seq2[Value, Value]
	// SetKey adds or replaces an entry.
	SetKey(t *Thread, k, v Value) error
	// Delete removes an entry, returning its value and whether it
	// was present.
	Delete(t *Thread, k Value) (Value, bool, error)
}

type dictEntry struct {
	// Nil for entries that have been deleted.
	key   Value
	value Value
}

// memDict is an in-memory dict. It is a hash table that preserves
// insertion order by storing entries in a slice.
type memDict struct {
	mutability
	entries []dictEntry
	// Maps hashes to indices in entries.
	index map[uint32][]int
	count int
}

var _ Dict = (*memDict)(nil)

// NewDict creates an empty mutable dict.
func NewDict() Dict {
	return &memDict{}
}

// NewImmutableDict creates an immutable dict containing the provided
// entries, which must also be immutable.
func NewImmutableDict(t *Thread, entries [][2]Value) (Dict, error) {
	d := &memDict{}
	for _, entry := range entries {
		if err := d.SetKey(t, entry[0], entry[1]); err != nil {
			return nil, err
		}
	}
	d.immutable = true
	return d, nil
}

func (*memDict) isDict() {}

// Type returns "dict".
func (*memDict) Type() string { return "dict" }

// Truth returns whether the dict is non-empty.
func (d *memDict) Truth(*Thread) (bool, error) { return d.count > 0, nil }

// Len returns the number of entries in the dict.
func (d *memDict) Len(*Thread) (int, error) { return d.count, nil }

// Repr appends the representation of the dict to the printer.
func (d *memDict) Repr(t *Thread, p *Printer) error {
	if !p.Enter(d) {
		p.WriteString("{...}")
		return nil
	}
	defer p.Leave()
	p.WriteString("{")
	first := true
	for _, entry := range d.entries {
		if entry.key == nil {
			continue
		}
		if !first {
			p.WriteString(", ")
		}
		first = false
		if err := entry.key.Repr(t, p); err != nil {
			return err
		}
		p.WriteString(": ")
		if err := entry.value.Repr(t, p); err != nil {
			return err
		}
	}
	p.WriteString("}")
	return nil
}

// lookup returns the index of the entry with the provided key and the
// hash of the key.
func (d *memDict) lookup(t *Thread, k Value) (int, uint32, error) {
	h, err := hashValue(t, k)
	if err != nil {
		return -1, 0, err
	}
	for _, i := range d.index[h] {
		if equal, err := Equal(t, d.entries[i].key, k); err != nil {
			return -1, 0, err
		} else if equal {
			return i, h, nil
		}
	}
	return -1, h, nil
}

// Get returns the value corresponding to a key.
func (d *memDict) Get(t *Thread, k Value) (Value, bool, error) {
	i, _, err := d.lookup(t, k)
	if err != nil || i < 0 {
		return nil, false, err
	}
	return d.entries[i].value, true, nil
}

// SetKey adds or replaces an entry.
func (d *memDict) SetKey(t *Thread, k, v Value) error {
	if err := d.checkMutable("dict"); err != nil {
		return err
	}
	i, h, err := d.lookup(t, k)
	if err != nil {
		return err
	}
	if i >= 0 {
		d.entries[i].value = v
		return nil
	}
	if d.index == nil {
		d.index = map[uint32][]int{}
	}
	d.index[h] = append(d.index[h], len(d.entries))
	d.entries = append(d.entries, dictEntry{key: k, value: v})
	d.count++
	return nil
}

// Delete removes an entry.
func (d *memDict) Delete(t *Thread, k Value) (Value, bool, error) {
	if err := d.checkMutable("dict"); err != nil {
		return nil, false, err
	}
	i, h, err := d.lookup(t, k)
	if err != nil || i < 0 {
		return nil, false, err
	}
	v := d.entries[i].value
	d.entries[i] = dictEntry{}
	d.count--

	indices := d.index[h]
	for j, index := range indices {
		if index == i {
			indices = append(indices[:j], indices[j+1:]...)
			break
		}
	}
	if len(indices) == 0 {
		delete(d.index, h)
	} else {
		d.index[h] = indices
	}

	// Compact the entries if more than half of them are deleted.
	if d.count < len(d.entries)/2 {
		d.compact(t)
	}
	return v, true, nil
}

// compact removes entries that have been deleted, and rebuilds the
// index.
func (d *memDict) compact(t *Thread) {
	entries := make([]dictEntry, 0, d.count)
	index := make(map[uint32][]int, d.count)
	for _, entry := range d.entries {
		if entry.key != nil {
			// Keys were hashed successfully before.
			h, _ := hashValue(t, entry.key)
			index[h] = append(index[h], len(entries))
			entries = append(entries, entry)
		}
	}
	d.entries = entries
	d.index = index
}

// Iterate over the keys of the dict. The dict cannot be modified while
// iteration is in progress.
func (d *memDict) Iterate(*Thread, *error) iter.Seq[Value] {
	return func(yield func(Value) bool) {
		d.iterators++
		defer func() { d.iterators-- }()
		for _, entry := range d.entries {
			if entry.key != nil && !yield(entry.key) {
				return
			}
		}
	}
}

// Items returns the entries of the dict in insertion order.
func (d *memDict) Items(*Thread, *error) iter.Seq2[Value, Value] {
	return func(yield func(Value, Value) bool) {
		d.iterators++
		defer func() { d.iterators-- }()
		for _, entry := range d.entries {
			if entry.key != nil && !yield(entry.key, entry.value) {
				return
			}
		}
	}
}

// CompareSameType compares dicts for equality. Dicts are equal if they
// contain the same entries, regardless of order.
func (d *memDict) CompareSameType(t *Thread, op syntax.Token, y Value, depth int) (bool, error) {
	switch op {
	case syntax.EQL, syntax.NEQ:
		equal, err := dictsEqual(t, d, y.(Dict), depth)
		if err != nil {
			return false, err
		}
		return equal == (op == syntax.EQL), nil
	}
	return false, unsupportedComparison(op, d, y)
}

func dictsEqual(t *Thread, x, y Dict, depth int) (bool, error) {
	xLen, err := x.Len(t)
	if err != nil {
		return false, err
	}
	yLen, err := y.Len(t)
	if err != nil {
		return false, err
	}
	if xLen != yLen {
		return false, nil
	}
	var errIter error
	for k, xv := range x.Items(t, &errIter) {
		yv, found, err := y.Get(t, k)
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
		if equal, err := equalDepth(t, xv, yv, depth-1); err != nil || !equal {
			return false, err
		}
	}
	return true, errIter
}
