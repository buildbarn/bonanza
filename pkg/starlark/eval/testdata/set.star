# Derived from go.starlark.net's starlark/testdata, which is
# Copyright 2017 The Bazel Authors. All rights reserved. Use of this
# source code is governed by the BSD-style license that can be found in
# LICENSE.go-starlark. Modifications have been made to match the
# behavior of Bazel.

# Tests of Starlark 'set'
# option:set option:globalreassign

# Sets are not a standard part of Starlark, so the features
# tested in this file must be enabled in the application by setting
# resolve.AllowSet.  (All sets are created by calls to the 'set'
# built-in or derived from operations on existing sets.)
# The semantics are subject to change as the spec evolves.

# TODO(adonovan): support set mutation:
# - del set[k]
# - set += iterable, perhaps?
# Test iterator invalidation.

load("assert.star", "asserts", "freeze")

# literals
# Parser does not currently support {1, 2, 3}.
# TODO(adonovan): add test to syntax/testdata/errors.star.

# set comprehensions
# Parser does not currently support {x for x in y}.
# See syntax/testdata/errors.star.

# set constructor
asserts.eq(type(set()), "set")
asserts.eq(list(set()), [])
asserts.eq(type(set([1, 3, 2, 3])), "set")
asserts.eq(list(set([1, 3, 2, 3])), [1, 3, 2])
asserts.eq(type(set("hello".elems())), "set")
asserts.eq(list(set("hello".elems())), ["h", "e", "l", "o"])
asserts.eq(list(set(range(3))), [0, 1, 2])
asserts.fails(lambda : set(1), "type 'int' is not iterable")
asserts.fails(lambda : set(1, 2, 3), "accepts no more than 1 positional argument \\(3 given\\)")
asserts.fails(lambda : set([1, 2, {}]), "unhashable type: dict")

# truth
asserts.true(not set())
asserts.true(set([False]))
asserts.true(set([1, 2, 3]))

x = set([1, 2, 3])
y = set([3, 4, 5])

# set + any is not defined
asserts.fails(lambda : x + y, "unsupported binary operation: set \\+ set")

# set | set
asserts.eq(list(set("a".elems()) | set("b".elems())), ["a", "b"])
asserts.eq(list(set("ab".elems()) | set("bc".elems())), ["a", "b", "c"])
asserts.fails(lambda : set() | [], "unknown binary op: set | list")
asserts.eq(type(x | y), "set")
asserts.eq(list(x | y), [1, 2, 3, 4, 5])
asserts.eq(list(x | set([5, 1])), [1, 2, 3, 5])
asserts.eq(list(x | set((6, 5, 4))), [1, 2, 3, 6, 5, 4])

# set.union (allows any iterable for right operand)
asserts.eq(list(set("a".elems()).union("b".elems())), ["a", "b"])
asserts.eq(list(set("ab".elems()).union("bc".elems())), ["a", "b", "c"])
asserts.eq(set().union([]), set())
asserts.eq(type(x.union(y)), "set")
asserts.eq(list(x.union()), [1, 2, 3])
asserts.eq(list(x.union(y)), [1, 2, 3, 4, 5])
asserts.eq(list(x.union(y, [6, 7])), [1, 2, 3, 4, 5, 6, 7])
asserts.eq(list(x.union([5, 1])), [1, 2, 3, 5])
asserts.eq(list(x.union((6, 5, 4))), [1, 2, 3, 6, 5, 4])
asserts.fails(lambda : x.union([1, 2, {}]), "unhashable type: dict")
asserts.fails(lambda : x.union(1, 2, 3), "type 'int' is not iterable")

# set.update (allows any iterable for the right operand)
# The update function will mutate the set so the tests below are
# scoped using a function.

def test_update_return_value():
    asserts.eq(set(x).update(y), None)

test_update_return_value()

def test_update_elems_singular():
    s = set("a".elems())
    s.update("b".elems())
    asserts.eq(list(s), ["a", "b"])

test_update_elems_singular()

def test_update_elems_multiple():
    s = set("a".elems())
    s.update("bc".elems())
    asserts.eq(list(s), ["a", "b", "c"])

test_update_elems_multiple()

def test_update_empty():
    s = set()
    s.update([])
    asserts.eq(s, set())

test_update_empty()

def test_update_set():
    s = set(x)
    s.update(y)
    asserts.eq(list(s), [1, 2, 3, 4, 5])

test_update_set()

def test_update_set_multiple_args():
    s = set(x)
    s.update([11, 12], [11, 13, 14])
    asserts.eq(list(s), [1, 2, 3, 11, 12, 13, 14])

test_update_set_multiple_args()

def test_update_list_intersecting():
    s = set(x)
    s.update([5, 1])
    asserts.eq(list(s), [1, 2, 3, 5])

test_update_list_intersecting()

def test_update_list_non_intersecting():
    s = set(x)
    s.update([6, 5, 4])
    asserts.eq(list(s), [1, 2, 3, 6, 5, 4])

test_update_list_non_intersecting()

def test_update_non_hashable():
    s = set(x)
    asserts.fails(lambda: x.update([1, 2, {}]), "unhashable type: dict")

test_update_non_hashable()

def test_update_non_iterable():
    s = set(x)
    asserts.fails(lambda: x.update(9), "type 'int' is not iterable")

test_update_non_iterable()

def test_update_kwargs():
    s = set(x)
    asserts.fails(lambda: x.update(gee = [3, 4]), "update\\(\\) got unexpected keyword argument 'gee'")

test_update_kwargs()

def test_update_no_arg():
    s = set(x)
    s.update()
    asserts.eq(list(s), [1, 2, 3])

test_update_no_arg()

# intersection, set & set or set.intersection(iterable)
asserts.eq(list(set("a".elems()) & set("b".elems())), [])
asserts.eq(list(set("ab".elems()) & set("bc".elems())), ["b"])
asserts.eq(list(set("a".elems()).intersection("b".elems())), [])
asserts.eq(list(set("ab".elems()).intersection("bc".elems())), ["b"])

# symmetric difference, set ^ set or set.symmetric_difference(iterable)
asserts.eq(set([1, 2, 3]) ^ set([4, 5, 3]), set([1, 2, 4, 5]))
asserts.eq(set([1,2,3,4]).symmetric_difference([3,4,5,6]), set([1,2,5,6]))
asserts.eq(set([1,2,3,4]).symmetric_difference(set([])), set([1,2,3,4]))

def test_set_augmented_assign():
    x = set([1, 2, 3])
    x &= set([2, 3])
    asserts.eq(x, set([2, 3]))
    x |= set([1])
    asserts.eq(x, set([1, 2, 3]))
    x ^= set([4, 5, 3])
    asserts.eq(x, set([1, 2, 4, 5]))

test_set_augmented_assign()

# len
asserts.eq(len(x), 3)
asserts.eq(len(y), 3)
asserts.eq(len(x | y), 5)

# str
asserts.eq(str(set([1])), "set([1])")
asserts.eq(str(set([2, 3])), "set([2, 3])")
asserts.eq(str(set([3, 2])), "set([3, 2])")

# comparison
asserts.eq(x, x)
asserts.eq(y, y)
asserts.true(x != y)
asserts.eq(set([1, 2, 3]), set([3, 2, 1]))

# iteration
asserts.true(type([elem for elem in x]), "list")
asserts.true(list([elem for elem in x]), [1, 2, 3])

def iter():
    list = []
    for elem in x:
        list.append(elem)
    return list

asserts.eq(iter(), [1, 2, 3])

# sets are not indexable
asserts.fails(lambda : x[0], "type 'set' has no operator")

# adding and removing
add_set = set([1,2,3])
add_set.add(4)
asserts.true(4 in add_set)
freeze(add_set)
asserts.fails(lambda: add_set.add(4), "trying to mutate a frozen set value")
asserts.fails(lambda: add_set.add(5), "trying to mutate a frozen set value")

# remove
remove_set = set([1,2,3])
remove_set.remove(3)
asserts.true(3 not in remove_set)
asserts.fails(lambda: remove_set.remove(3), "remove\\(\\): element 3 not found")
freeze(remove_set)
asserts.fails(lambda: remove_set.remove(3), "trying to mutate a frozen set value")

# discard
discard_set = set([1,2,3])
discard_set.discard(3)
asserts.true(3 not in discard_set)
asserts.eq(discard_set.discard(3), None)
freeze(discard_set)
asserts.fails(lambda: discard_set.discard(3), "trying to mutate a frozen set value")
asserts.fails(lambda: discard_set.discard(1), "trying to mutate a frozen set value")

# update
update_set = set([1, 2, 3])
update_set.update([4])
asserts.true(4 in update_set)
freeze(update_set)
asserts.fails(lambda: update_set.update([5]), "trying to mutate a frozen set value")

# pop
pop_set = set([1,2,3])
asserts.eq(pop_set.pop(), 1)
asserts.eq(pop_set.pop(), 2)
asserts.eq(pop_set.pop(), 3)
asserts.fails(lambda: pop_set.pop(), "pop\\(\\): set is empty")
pop_set.add(1)
pop_set.add(2)
freeze(pop_set)
asserts.fails(lambda: pop_set.pop(), "trying to mutate a frozen set value")

# clear
clear_set = set([1,2,3])
clear_set.clear()
asserts.eq(len(clear_set), 0)
freeze(clear_set)
asserts.fails(lambda: clear_set.clear(), "trying to mutate a frozen set value")

other_clear_set = set([1,2,3])
freeze(other_clear_set)
asserts.fails(lambda: other_clear_set.clear(), "trying to mutate a frozen set value")

# difference: set - set or set.difference(iterable)
asserts.eq(set([1,2,3,4]).difference([1,2,3,4]), set([]))
asserts.eq(set([1,2,3,4]).difference([1,2]), set([3,4]))
asserts.eq(set([1,2,3,4]).difference([]), set([1,2,3,4]))
asserts.eq(set([1,2,3,4]).difference(set([1,2,3])), set([4]))

asserts.eq(set([1,2,3,4]) - set([1,2,3,4]), set())
asserts.eq(set([1,2,3,4]) - set([1,2]), set([3,4]))

# issuperset: set >= set or set.issuperset(iterable)
asserts.true(set([1,2,3]).issuperset([1,2]))
asserts.true(not set([1,2,3]).issuperset(set([1,2,4])))
asserts.true(set([1,2,3]) >= set([1,2,3]))
asserts.true(set([1,2,3]) >= set([1,2]))
asserts.true(not set([1,2,3]) >= set([1,2,4]))

# proper superset: set > set
asserts.true(set([1, 2, 3]) > set([1, 2]))
asserts.true(not set([1,2, 3]) > set([1, 2, 3]))

# issubset: set <= set or set.issubset(iterable)
asserts.true(set([1,2]).issubset([1,2,3]))
asserts.true(not set([1,2,3]).issubset(set([1,2,4])))
asserts.true(set([1,2,3]) <= set([1,2,3]))
asserts.true(set([1,2]) <= set([1,2,3]))
asserts.true(not set([1,2,3]) <= set([1,2,4]))

# proper subset: set < set
asserts.true(set([1,2]) < set([1,2,3]))
asserts.true(not set([1,2,3]) < set([1,2,3]))
