# Derived from go.starlark.net's starlark/testdata, which is
# Copyright 2017 The Bazel Authors. All rights reserved. Use of this
# source code is governed by the BSD-style license that can be found in
# LICENSE.go-starlark. Modifications have been made to match the
# behavior of Bazel.

# Tests of Starlark 'list'

load("assert.star", "asserts", "freeze")

# literals
asserts.eq([], [])
asserts.eq([1], [1])
asserts.eq([1], [1])
asserts.eq([1, 2], [1, 2])
asserts.ne([1, 2, 3], [1, 2, 4])

# truth
asserts.true([0])
asserts.true(not [])

# indexing, x[i]
abc = list("abc".elems())
asserts.fails(lambda: abc[-4], "index out of range \\(index is -4, but sequence has 3 elements\\)")
asserts.eq(abc[-3], "a")
asserts.eq(abc[-2], "b")
asserts.eq(abc[-1], "c")
asserts.eq(abc[0], "a")
asserts.eq(abc[1], "b")
asserts.eq(abc[2], "c")
asserts.fails(lambda: abc[3], "index out of range \\(index is 3, but sequence has 3 elements\\)")

# x[i] = ...
x3 = [0, 1, 2]
x3[1] = 2
x3[2] += 3
asserts.eq(x3, [0, 2, 5])

def f2():
    x3[3] = 4

asserts.fails(f2, "out of range")
freeze(x3)

def f3():
    x3[0] = 0

asserts.fails(f3, "trying to mutate a frozen list value")
asserts.fails(x3.clear, "trying to mutate a frozen list value")

# list + list
asserts.eq([1, 2, 3] + [3, 4, 5], [1, 2, 3, 3, 4, 5])
asserts.fails(lambda: [1, 2] + (3, 4), "unsupported binary operation: list \\+ tuple")
asserts.fails(lambda: (1, 2) + [3, 4], "unsupported binary operation: tuple \\+ list")

# list * int,  int * list
asserts.eq(abc * 0, [])
asserts.eq(abc * -1, [])
asserts.eq(abc * 1, abc)
asserts.eq(abc * 3, ["a", "b", "c", "a", "b", "c", "a", "b", "c"])
asserts.eq(0 * abc, [])
asserts.eq(-1 * abc, [])
asserts.eq(1 * abc, abc)
asserts.eq(3 * abc, ["a", "b", "c", "a", "b", "c", "a", "b", "c"])

# list comprehensions
asserts.eq([2 * x for x in [1, 2, 3]], [2, 4, 6])
asserts.eq([2 * x for x in [1, 2, 3] if x > 1], [4, 6])
asserts.eq(
    [(x, y) for x in [1, 2] for y in [3, 4]],
    [(1, 3), (1, 4), (2, 3), (2, 4)],
)
asserts.eq([(x, y) for x in [1, 2] if x == 2 for y in [3, 4]], [(2, 3), (2, 4)])
asserts.eq([2 * x for x in (1, 2, 3)], [2, 4, 6])
asserts.eq([x for x in "abc".elems()], ["a", "b", "c"])
asserts.eq([x for x in {"a": 1, "b": 2}], ["a", "b"])
asserts.eq([(y, x) for x, y in {1: 2, 3: 4}.items()], [(2, 1), (4, 3)])

# corner cases of parsing:
asserts.eq([x for x in range(12) if x % 2 == 0 if x % 3 == 0], [0, 6])
asserts.eq([x for x in [1, 2] if lambda: None], [1, 2])
asserts.eq([x for x in [1, 2] if (lambda: 3 if True else 4)], [1, 2])

# list function
asserts.eq(list(), [])
asserts.eq(list("ab".elems()), ["a", "b"])

# A list comprehension defines a separate lexical block,
# whether at top-level...
a = [1, 2]
b = [a for a in [3, 4]]
asserts.eq(a, [1, 2])
asserts.eq(b, [3, 4])

# ...or local to a function.
def listcompblock():
    c = [1, 2]
    d = [c for c in [3, 4]]
    asserts.eq(c, [1, 2])
    asserts.eq(d, [3, 4])

listcompblock()

# list.pop
x4 = [1, 2, 3, 4, 5]
asserts.fails(lambda: x4.pop(-6), "index out of range \\(index is -6, but sequence has 5 elements\\)")
asserts.fails(lambda: x4.pop(6), "index out of range \\(index is 6, but sequence has 5 elements\\)")
asserts.eq(x4.pop(), 5)
asserts.eq(x4, [1, 2, 3, 4])
asserts.eq(x4.pop(1), 2)
asserts.eq(x4, [1, 3, 4])
asserts.eq(x4.pop(0), 1)
asserts.eq(x4, [3, 4])
asserts.eq(x4.pop(-2), 3)
asserts.eq(x4, [4])
asserts.eq(x4.pop(-1), 4)
asserts.eq(x4, [])

# TODO(adonovan): test uses of list as sequence
# (for loop, comprehension, library functions).

# x += y for lists is equivalent to x.extend(y).
# y may be a sequence.
# TODO: Test that side-effects of 'x' occur only once.
def list_extend():
    a = [1, 2, 3]
    b = a
    a = a + [4]  # creates a new list
    asserts.eq(a, [1, 2, 3, 4])
    asserts.eq(b, [1, 2, 3])  # b is unchanged

    a = [1, 2, 3]
    b = a
    a += [4]  # updates a (and thus b) in place
    asserts.eq(a, [1, 2, 3, 4])
    asserts.eq(b, [1, 2, 3, 4])  # alias observes the change

    a = [1, 2, 3]
    b = a
    a.extend([4])  # updates existing list
    asserts.eq(a, [1, 2, 3, 4])
    asserts.eq(b, [1, 2, 3, 4])  # alias observes the change

list_extend()

# Unlike list.extend(iterable), list += iterable makes its LHS name local.
a_list = []

def f4():
    a_list += [1]  # binding use => a_list is a local var

asserts.fails(f4, "local variable a_list referenced before assignment")

# list += <not iterable>
def f5():
    x = []
    x += 1

asserts.fails(f5, "unsupported binary operation: list \\+ int")

# frozen list += iterable
def f6():
    x = []
    freeze(x)
    x += [1]

asserts.fails(f6, "trying to mutate a frozen list value")

# list += hasfields (hasfields is not iterable but defines list+hasfields)
def f7():
    x = []
    x += hasfields()
    return x

asserts.eq(f7(), 42)  # weird, but exercises a corner case in list+=x.

# append
x5 = [1, 2, 3]
x5.append(4)
x5.append("abc")
asserts.eq(x5, [1, 2, 3, 4, "abc"])

# extend
x5a = [1, 2, 3]
x5a.extend("abc".elems())  # string
x5a.extend((True, False))  # tuple
asserts.eq(x5a, [1, 2, 3, "a", "b", "c", True, False])

# list.insert
def insert_at(index):
    x = list(range(3))
    x.insert(index, 42)
    return x

asserts.eq(insert_at(-99), [42, 0, 1, 2])
asserts.eq(insert_at(-2), [0, 42, 1, 2])
asserts.eq(insert_at(-1), [0, 1, 42, 2])
asserts.eq(insert_at(0), [42, 0, 1, 2])
asserts.eq(insert_at(1), [0, 42, 1, 2])
asserts.eq(insert_at(2), [0, 1, 42, 2])
asserts.eq(insert_at(3), [0, 1, 2, 42])
asserts.eq(insert_at(4), [0, 1, 2, 42])

# list.remove
def remove(v):
    x = [3, 1, 4, 1]
    x.remove(v)
    return x

asserts.eq(remove(3), [1, 4, 1])
asserts.eq(remove(1), [3, 4, 1])
asserts.eq(remove(4), [3, 1, 1])
asserts.fails(lambda: [3, 1, 4, 1].remove(42), "remove\\(\\): element 42 not found")

# list.index
bananas = list("bananas".elems())
asserts.eq(bananas.index("a"), 1)  # bAnanas
asserts.fails(lambda: bananas.index("d"), "not found in list")

# start
asserts.eq(bananas.index("a", -1000), 1)  # bAnanas
asserts.eq(bananas.index("a", 0), 1)  # bAnanas
asserts.eq(bananas.index("a", 1), 1)  # bAnanas
asserts.eq(bananas.index("a", 2), 3)  # banAnas
asserts.eq(bananas.index("a", 3), 3)  # banAnas
asserts.eq(bananas.index("b", 0), 0)  # Bananas
asserts.eq(bananas.index("n", -3), 4)  # banaNas
asserts.fails(lambda: bananas.index("n", -2), "not found in list")
asserts.eq(bananas.index("s", -2), 6)  # bananaS
asserts.fails(lambda: bananas.index("b", 1), "not found in list")

# start, end
asserts.eq(bananas.index("s", -1000, 7), 6)  # bananaS
asserts.fails(lambda: bananas.index("s", -1000, 6), "not found in list")
asserts.fails(lambda: bananas.index("d", -1000, 1000), "not found in list")

# slicing, x[i:j:k]
asserts.eq(bananas[6::-2], list("snnb".elems()))
asserts.eq(bananas[5::-2], list("aaa".elems()))
asserts.eq(bananas[4::-2], list("nnb".elems()))
asserts.eq(bananas[99::-2], list("snnb".elems()))
asserts.eq(bananas[100::-2], list("snnb".elems()))
# TODO(adonovan): many more tests

# iterator invalidation
def iterator1():
    list = [0, 1, 2]
    for x in list:
        list[x] = 2 * x
    return list

asserts.fails(iterator1, "temporarily immutable due to active for-loop iteration")

def iterator2():
    list = [0, 1, 2]
    for x in list:
        list.remove(x)

asserts.fails(iterator2, "temporarily immutable due to active for-loop iteration")

def iterator3():
    list = [0, 1, 2]
    for x in list:
        list.append(3)

asserts.fails(iterator3, "temporarily immutable due to active for-loop iteration")

def iterator4():
    list = [0, 1, 2]
    for x in list:
        list.extend([3, 4])

asserts.fails(iterator4, "temporarily immutable due to active for-loop iteration")

def iterator5():
    def f(x):
        x.append(4)

    list = [1, 2, 3]
    _ = [f(list) for x in list]

asserts.fails(iterator5, "temporarily immutable due to active for-loop iteration")
