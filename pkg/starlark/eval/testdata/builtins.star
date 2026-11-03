# Derived from go.starlark.net's starlark/testdata, which is
# Copyright 2017 The Bazel Authors. All rights reserved. Use of this
# source code is governed by the BSD-style license that can be found in
# LICENSE.go-starlark. Modifications have been made to match the
# behavior of Bazel.

# Tests of Starlark built-in functions
# option:set

load("assert.star", "asserts")

# len
asserts.eq(len([1, 2, 3]), 3)
asserts.eq(len((1, 2, 3)), 3)
asserts.eq(len({1: 2}), 1)
asserts.fails(lambda: len(1), "int is not iterable")

# and, or
asserts.eq(123 or "foo", 123)
asserts.eq(0 or "foo", "foo")
asserts.eq(123 and "foo", "foo")
asserts.eq(0 and "foo", 0)
none = None
_1 = none and none[0]      # rhs is not evaluated
_2 = (not none) or none[0] # rhs is not evaluated

# abs
asserts.eq(abs(2.0), 2.0)
asserts.eq(abs(0.0), 0.0)
asserts.eq(abs(-2.0), 2.0)
asserts.eq(abs(2), 2)
asserts.eq(abs(0), 0)
asserts.eq(abs(-2), 2)
asserts.eq(abs(float("inf")), float("inf"))
asserts.eq(abs(float("-inf")), float("inf"))
asserts.eq(abs(float("nan")), float("nan"))
asserts.fails(lambda: abs("0"), "got string, want int or float")
maxint32 = (1 << 31) - 1
asserts.eq(abs(+123 * maxint32), +123 * maxint32)
asserts.eq(abs(-123 * maxint32), +123 * maxint32)

# any, all
asserts.true(all([]))
asserts.true(all([1, True, "foo"]))
asserts.true(not all([1, True, ""]))
asserts.true(not any([]))
asserts.true(any([0, False, "foo"]))
asserts.true(not any([0, False, ""]))

# in
asserts.true(3 in [1, 2, 3])
asserts.true(4 not in [1, 2, 3])
asserts.true(3 in (1, 2, 3))
asserts.true(4 not in (1, 2, 3))
asserts.fails(lambda: 3 in "foo", "in.*requires string as left operand")
asserts.true(123 in {123: ""})
asserts.true(456 not in {123:""})
asserts.fails(lambda: [] not in {123: ""}, "unhashable type: list")

# sorted
asserts.eq(sorted([42, 123, 3]), [3, 42, 123])
asserts.eq(sorted([42, 123, 3], reverse=True), [123, 42, 3])
asserts.eq(sorted(["wiz", "foo", "bar"]), ["bar", "foo", "wiz"])
asserts.eq(sorted(["wiz", "foo", "bar"], reverse=True), ["wiz", "foo", "bar"])
asserts.fails(lambda: sorted([1, 2, None, 3]), "unsupported comparison: NoneType < int")
asserts.fails(lambda: sorted([1, "one"]), "unsupported comparison: string < int")
# custom key function
asserts.eq(sorted(["two", "three", "four"], key=len),
          ["two", "four", "three"])
asserts.eq(sorted(["two", "three", "four"], key=len, reverse=True),
          ["three", "four", "two"])
asserts.eq(sorted([3, 1, 2], key=None), [1, 2, 3])
# sort is stable
pairs = [(4, 0), (3, 1), (4, 2), (2, 3), (3, 4), (1, 5), (2, 6), (3, 7)]
asserts.eq(sorted(pairs, key=lambda x: x[0]),
          [(1, 5),
           (2, 3), (2, 6),
           (3, 1), (3, 4), (3, 7),
           (4, 0), (4, 2)])
asserts.fails(lambda: sorted(1), "type 'int' is not iterable")

# reversed
asserts.eq(reversed([1, 144, 81, 16]), [16, 81, 144, 1])

# set
asserts.contains(set([1, 2, 3]), 1)
asserts.true(4 not in set([1, 2, 3]))
asserts.eq(len(set([1, 2, 3])), 3)
asserts.eq(sorted([x for x in set([1, 2, 3])]), [1, 2, 3])

# dict
asserts.eq(dict([(1, 2), (3, 4)]), {1: 2, 3: 4})
asserts.eq(dict([(1, 2), (3, 4)], foo="bar"), {1: 2, 3: 4, "foo": "bar"})
asserts.eq(dict({1:2, 3:4}), {1: 2, 3: 4})
asserts.eq(dict({1:2, 3:4}.items()), {1: 2, 3: 4})

# range
asserts.eq("range", type(range(10)))
asserts.eq("range(0, 10)", str(range(0, 10, 1)))
asserts.eq("range(1, 10)", str(range(1, 10)))
asserts.eq(range(0, 5, 10), range(0, 5, 11))
asserts.eq("range(0, 10, -1)", str(range(0, 10, -1)))
asserts.fails(lambda: {range(10): 10}, "unhashable type: range")
asserts.true(bool(range(1, 2)))
asserts.true(not(range(2, 1))) # an empty range is false
asserts.eq([x*x for x in range(5)], [0, 1, 4, 9, 16])
asserts.eq(list(range(5)), [0, 1, 2, 3, 4])
asserts.eq(list(range(-5)), [])
asserts.eq(list(range(2, 5)), [2, 3, 4])
asserts.eq(list(range(5, 2)), [])
asserts.eq(list(range(-2, -5)), [])
asserts.eq(list(range(-5, -2)), [-5, -4, -3])
asserts.eq(list(range(2, 10, 3)), [2, 5, 8])
asserts.eq(list(range(10, 2, -3)), [10, 7, 4])
asserts.eq(list(range(-2, -10, -3)), [-2, -5, -8])
asserts.eq(list(range(-10, -2, 3)), [-10, -7, -4])
asserts.eq(list(range(10, 2, -1)), [10, 9, 8, 7, 6, 5, 4, 3])
asserts.eq(list(range(5)[1:]), [1, 2, 3, 4])
asserts.eq(len(range(5)[1:]), 4)
asserts.eq(list(range(5)[:2]), [0, 1])
asserts.eq(list(range(10)[1:]), [1, 2, 3, 4, 5, 6, 7, 8, 9])
asserts.eq(list(range(10)[1:9:2]), [1, 3, 5, 7])
asserts.eq(list(range(10)[1:10:2]), [1, 3, 5, 7, 9])
asserts.eq(list(range(10)[1:11:2]), [1, 3, 5, 7, 9])
asserts.eq(list(range(10)[::-2]), [9, 7, 5, 3, 1])
asserts.eq(list(range(0, 10, 2)[::2]), [0, 4, 8])
asserts.eq(list(range(0, 10, 2)[::-2]), [8, 4, 0])
# range() is limited by the width of the Go int type (int32 or int64).
asserts.fails(lambda: range(1<<64), "out of range")
asserts.eq(len(range(0x7fffffff)), 0x7fffffff) # O(1)
# Two ranges compare equal if they denote the same sequence:
asserts.eq(range(0), range(2, 1, 3))       # []
asserts.eq(range(0, 3, 2), range(0, 4, 2)) # [0, 2]
asserts.ne(range(1, 10), range(2, 10))
asserts.fails(lambda: range(0) < range(0), "unsupported comparison: range < range")
# <number> in <range>
asserts.contains(range(3), 1)
asserts.contains(range(3), 2.0)    # acts like 2
asserts.fails(lambda: True in range(3), "requires integer as left operand, not bool") # bools aren't numbers
asserts.fails(lambda: "one" in range(10), "requires integer as left operand, not string")
asserts.true(4 not in range(4))
asserts.true(1e15 not in range(4)) # too big for int32
asserts.true(1e100 not in range(4)) # too big for int64
# https://github.com/google/starlark-go/issues/116
asserts.fails(lambda: range(0, 0, 2)[:][0], "index out of range \\(index is 0, but sequence has 0 elements\\)")

# list
asserts.eq(list("abc".elems()), ["a", "b", "c"])
asserts.eq(sorted(list({"a": 1, "b": 2})), ['a', 'b'])

# min, max
asserts.eq(min(5, -2, 1, 7, 3), -2)
asserts.eq(max(5, -2, 1, 7, 3), 7)
asserts.eq(min([5, -2, 1, 7, 3]), -2)
asserts.eq(min("one", "two", "three", "four"), "four")
asserts.eq(max("one", "two", "three", "four"), "two")
asserts.fails(min, "min\\(\\) requires at least one positional argument")
asserts.fails(lambda: min(1), "not iterable")
asserts.fails(lambda: min([]), "empty")
asserts.eq(min(5, -2, 1, 7, 3, key=lambda x: x*x), 1) # min absolute value
asserts.eq(min(5, -2, 1, 7, 3, key=lambda x: -x), 7) # min negated value

# enumerate
asserts.eq(enumerate("abc".elems()), [(0, "a"), (1, "b"), (2, "c")])
asserts.eq(enumerate([False, True, None], 42), [(42, False), (43, True), (44, None)])

# zip
asserts.eq(zip(), [])
asserts.eq(zip([]), [])
asserts.eq(zip([1, 2, 3]), [(1,), (2,), (3,)])
asserts.eq(zip("".elems()), [])
asserts.eq(zip("abc".elems(),
              list("def".elems()),
              "hijk".elems()),
          [("a", "d", "h"), ("b", "e", "i"), ("c", "f", "j")])
z1 = [1]
asserts.eq(zip(z1), [(1,)])
z1.append(2)
asserts.eq(zip(z1), [(1,), (2,)])
asserts.fails(lambda: zip(z1, 1), "type 'int' is not iterable")
z1.append(3)

# dir for builtin_function_or_method
asserts.eq(dir(None), [])
asserts.eq(dir({})[:3], ["clear", "get", "items"]) # etc
asserts.eq(dir(1), [])
asserts.eq(dir([])[:3], ["append", "clear", "extend"]) # etc

# hasattr, getattr, dir
# hasfields is an application-defined type defined in eval_test.go.
hf = hasfields()
asserts.eq(dir(hf), [])
asserts.true(not hasattr(hf, "x"))
asserts.fails(lambda: getattr(hf, "x"), "has no field or method 'x'")
asserts.eq(getattr(hf, "x", 42), 42)
hf.x = 1
asserts.true(hasattr(hf, "x"))
asserts.eq(getattr(hf, "x"), 1)
asserts.eq(hf.x, 1)
hf.x = 2
asserts.eq(getattr(hf, "x"), 2)
asserts.eq(hf.x, 2)
# built-in types can have attributes (methods) too.
myset = set([])
asserts.eq(dir(myset), ["add", "clear", "difference", "difference_update", "discard", "intersection", "intersection_update", "isdisjoint", "issubset", "issuperset", "pop", "remove", "symmetric_difference", "symmetric_difference_update", "union", "update"])
asserts.true(hasattr(myset, "union"))
asserts.true(not hasattr(myset, "onion"))
asserts.eq(str(getattr(myset, "union")), "<built-in method union of set value>")
asserts.fails(lambda: getattr(myset, "onion"), "has no field or method 'onion'")
asserts.eq(getattr(myset, "onion", 42), 42)

# dir returns a new, sorted, mutable list
asserts.eq(sorted(dir("")), dir("")) # sorted
dir("").append("!") # mutable
asserts.true("!" not in dir("")) # new

# error messages should suggest spelling corrections
hf.one = 1
hf.two = 2
hf.three = 3
hf.forty_five = 45
asserts.fails(lambda: hf.One, "has no field or method 'One' \\(did you mean 'one'\\?\\)")
asserts.fails(lambda: hf.oone, "has no field or method 'oone' \\(did you mean 'one'\\?\\)")
asserts.fails(lambda: hf.FortyFive, "has no field or method 'FortyFive' \\(did you mean 'forty_five'\\?\\)")
asserts.fails(lambda: hf.trhee, "has no field or method 'trhee' \\(did you mean 'three'\\?\\)")
asserts.fails(lambda: hf.thirty, "has no field or method 'thirty' \\(did you mean 'three'\\?\\)")

# repr
asserts.eq(repr(1), "1")
asserts.eq(repr("x"), '"x"')
asserts.eq(repr(["x", 1]), '["x", 1]')

# fail
---
fail() ### `^$`
x = 1//0 # unreachable
---
fail(1) ### `^1$`
---
fail(1, 2, 3) ### `^1 2 3$`
---
fail(1, 2, 3, sep="/") ### `^1/2/3$`
