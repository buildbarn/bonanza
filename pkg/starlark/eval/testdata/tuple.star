# Derived from go.starlark.net's starlark/testdata, which is
# Copyright 2017 The Bazel Authors. All rights reserved. Use of this
# source code is governed by the BSD-style license that can be found in
# LICENSE.go-starlark. Modifications have been made to match the
# behavior of Bazel.

# Tests of Starlark 'tuple'

load("assert.star", "asserts")

# literal
asserts.eq((), ())
asserts.eq((1), 1)
asserts.eq((1,), (1,))
asserts.ne((1), (1,))
asserts.eq((1, 2), (1, 2))
asserts.eq((1, 2, 3, 4, 5), (1, 2, 3, 4, 5))
asserts.ne((1, 2, 3), (1, 2, 4))

# truth
asserts.true((False,))
asserts.true((False, False))
asserts.true(not ())

# indexing, x[i]
asserts.eq(("a", "b")[0], "a")
asserts.eq(("a", "b")[1], "b")

# slicing, x[i:j]
asserts.eq("abcd"[0:4:1], "abcd")
asserts.eq("abcd"[::2], "ac")
asserts.eq("abcd"[1::2], "bd")
asserts.eq("abcd"[4:0:-1], "dcb")
banana = tuple("banana".elems())
asserts.eq(banana[7::-2], tuple("aaa".elems()))
asserts.eq(banana[6::-2], tuple("aaa".elems()))
asserts.eq(banana[5::-2], tuple("aaa".elems()))
asserts.eq(banana[4::-2], tuple("nnb".elems()))

# tuple
asserts.eq(tuple(), ())
asserts.eq(tuple("abc".elems()), ("a", "b", "c"))
asserts.eq(tuple(["a", "b", "c"]), ("a", "b", "c"))
asserts.eq(tuple([1]), (1,))
asserts.fails(lambda: tuple(1), "type 'int' is not iterable")

# tuple * int,  int * tuple
abc = tuple("abc".elems())
asserts.eq(abc * 0, ())
asserts.eq(abc * -1, ())
asserts.eq(abc * 1, abc)
asserts.eq(abc * 3, ("a", "b", "c", "a", "b", "c", "a", "b", "c"))
asserts.eq(0 * abc, ())
asserts.eq(-1 * abc, ())
asserts.eq(1 * abc, abc)
asserts.eq(3 * abc, ("a", "b", "c", "a", "b", "c", "a", "b", "c"))
asserts.fails(lambda: abc * (1000000 * 1000000), "repeat count too large")
asserts.fails(lambda: abc * 1000000 * 1000000, "repeated tuple too large")

# TODO(adonovan): test use of tuple as sequence
# (for loop, comprehension, library functions).
