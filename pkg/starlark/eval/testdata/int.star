# Derived from go.starlark.net's starlark/testdata, which is
# Copyright 2017 The Bazel Authors. All rights reserved. Use of this
# source code is governed by the BSD-style license that can be found in
# LICENSE.go-starlark. Modifications have been made to match the
# behavior of Bazel.

# Tests of Starlark 'int'

load("assert.star", "asserts")

# basic arithmetic
asserts.eq(0 - 1, -1)
asserts.eq(0 + 1, +1)
asserts.eq(1 + 1, 2)
asserts.eq(5 + 7, 12)
asserts.eq(5 * 7, 35)
asserts.eq(5 - 7, -2)

# int boundaries
maxint64 = (1 << 63) - 1
minint64 = -1 << 63
maxint32 = (1 << 31) - 1
minint32 = -1 << 31
asserts.eq(maxint64, 9223372036854775807)
asserts.eq(minint64, -9223372036854775808)
asserts.eq(maxint32, 2147483647)
asserts.eq(minint32, -2147483648)

# truth
def truth():
    asserts.true(not 0)
    for m in [1, maxint32]:  # Test small/big ranges
        asserts.true(123 * m)
        asserts.true(-1 * m)

truth()

# floored division
# (For real division, see float.star.)
def division():
    for m in [1, maxint32]:  # Test small/big ranges
        asserts.eq((100 * m) // (7 * m), 14)
        asserts.eq((100 * m) // (-7 * m), -15)
        asserts.eq((-100 * m) // (7 * m), -15)  # NB: different from Go/Java
        asserts.eq((-100 * m) // (-7 * m), 14)  # NB: different from Go/Java
        asserts.eq((98 * m) // (7 * m), 14)
        asserts.eq((98 * m) // (-7 * m), -14)
        asserts.eq((-98 * m) // (7 * m), -14)
        asserts.eq((-98 * m) // (-7 * m), 14)

division()

# remainder
def remainder():
    for m in [1, maxint32]:  # Test small/big ranges
        asserts.eq((100 * m) % (7 * m), 2 * m)
        asserts.eq((100 * m) % (-7 * m), -5 * m)  # NB: different from Go/Java
        asserts.eq((-100 * m) % (7 * m), 5 * m)  # NB: different from Go/Java
        asserts.eq((-100 * m) % (-7 * m), -2 * m)
        asserts.eq((98 * m) % (7 * m), 0)
        asserts.eq((98 * m) % (-7 * m), 0)
        asserts.eq((-98 * m) % (7 * m), 0)
        asserts.eq((-98 * m) % (-7 * m), 0)

remainder()

# compound assignment
def compound():
    x = 1
    x += 1
    asserts.eq(x, 2)
    x -= 3
    asserts.eq(x, -1)
    x *= 39
    asserts.eq(x, -39)
    x //= 4
    asserts.eq(x, -10)
    x /= -2
    asserts.eq(x, 5)
    x %= 3
    asserts.eq(x, 2)

    x = 2
    x &= 1
    asserts.eq(x, 0)
    x |= 2
    asserts.eq(x, 2)
    x ^= 3
    asserts.eq(x, 1)
    x <<= 2
    asserts.eq(x, 4)
    x >>= 2
    asserts.eq(x, 1)

compound()

# int conversion
# See float.star for float-to-int conversions.
# We follow Python 3 here, but I can't see the method in its madness.
# int from bool/int/float
asserts.fails(int, "missing required argument 'x'")  # int()
asserts.eq(int(False), 0)
asserts.eq(int(True), 1)
asserts.eq(int(3), 3)
asserts.eq(int(3.1), 3)
asserts.fails(lambda: int(3, base = 10), "non-string with explicit base")
asserts.fails(lambda: int(True, 10), "non-string with explicit base")

# int from string, base implicitly 10
asserts.eq(int("100000000000000000000"), 10000000000 * 10000000000)
asserts.eq(int("-100000000000000000000"), -10000000000 * 10000000000)
asserts.eq(int("123"), 123)
asserts.eq(int("-123"), -123)
asserts.eq(int("0123"), 123)  # not octal
asserts.eq(int("-0123"), -123)
asserts.fails(lambda: int("0x12"), "invalid literal for int\\(\\) with base 10")
asserts.fails(lambda: int("-0x12"), "invalid literal for int\\(\\) with base 10")
asserts.fails(lambda: int("0o123"), "invalid literal.*base 10")
asserts.fails(lambda: int("-0o123"), "invalid literal.*base 10")

# int from string, explicit base
asserts.eq(int("0"), 0)
asserts.eq(int("00"), 0)
asserts.eq(int("0", base = 10), 0)
asserts.eq(int("00", base = 10), 0)
asserts.eq(int("0", base = 8), 0)
asserts.eq(int("00", base = 8), 0)
asserts.eq(int("-0"), 0)
asserts.eq(int("-00"), 0)
asserts.eq(int("-0", base = 10), 0)
asserts.eq(int("-00", base = 10), 0)
asserts.eq(int("-0", base = 8), 0)
asserts.eq(int("-00", base = 8), 0)
asserts.eq(int("+0"), 0)
asserts.eq(int("+00"), 0)
asserts.eq(int("+0", base = 10), 0)
asserts.eq(int("+00", base = 10), 0)
asserts.eq(int("+0", base = 8), 0)
asserts.eq(int("+00", base = 8), 0)
asserts.eq(int("11", base = 9), 10)
asserts.eq(int("-11", base = 9), -10)
asserts.eq(int("10011", base = 2), 19)
asserts.eq(int("-10011", base = 2), -19)
asserts.eq(int("123", 8), 83)
asserts.eq(int("-123", 8), -83)
asserts.eq(int("0123", 8), 83)  # redundant zeros permitted
asserts.eq(int("-0123", 8), -83)
asserts.eq(int("00123", 8), 83)
asserts.eq(int("-00123", 8), -83)
asserts.eq(int("0o123", 8), 83)
asserts.eq(int("-0o123", 8), -83)
asserts.eq(int("123", 7), 66)  # 1*7*7 + 2*7 + 3
asserts.eq(int("-123", 7), -66)
asserts.eq(int("12", 16), 18)
asserts.eq(int("-12", 16), -18)
asserts.eq(int("0x12", 16), 18)
asserts.eq(int("-0x12", 16), -18)
asserts.eq(0x1000000000000001 * 0x1000000000000001, 0x1000000000000002000000000000001)
asserts.eq(int("1010", 2), 10)
asserts.eq(int("111111101", 2), 509)
asserts.eq(int("0b0101", 0), 5)
asserts.eq(int("0b0101", 2), 5) # prefix is redundant with explicit base
asserts.eq(int("0b00000", 0), 0)
asserts.eq(1111111111111111 * 1111111111111111, 1234567901234567654320987654321)
asserts.fails(lambda: int("0x123", 8), "invalid literal.*base 8")
asserts.fails(lambda: int("-0x123", 8), "invalid literal.*base 8")
asserts.fails(lambda: int("0o123", 16), "invalid literal.*base 16")
asserts.fails(lambda: int("-0o123", 16), "invalid literal.*base 16")
asserts.fails(lambda: int("0x110", 2), "invalid literal.*base 2")

# Base prefix is honored only if base=0, or if the prefix matches the explicit base.
# See https://github.com/google/starlark-go/issues/337
asserts.fails(lambda: int("0b0"), "invalid literal.*base 10")
asserts.eq(int("0b0", 0), 0)
asserts.eq(int("0b0", 2), 0)
asserts.eq(int("0b0", 16), 0xb0)
asserts.eq(int("0x0b0", 16), 0xb0)
asserts.eq(int("0x0b0", 0), 0xb0)
asserts.eq(int("0x0b0101", 16), 0x0b0101)

# int from string, auto detect base
asserts.eq(int("123", 0), 123)
asserts.eq(int("+123", 0), +123)
asserts.eq(int("-123", 0), -123)
asserts.eq(int("0x12", 0), 18)
asserts.eq(int("+0x12", 0), +18)
asserts.eq(int("-0x12", 0), -18)
asserts.eq(int("0o123", 0), 83)
asserts.eq(int("+0o123", 0), +83)
asserts.eq(int("-0o123", 0), -83)
asserts.fails(lambda: int("0123", 0), "invalid literal.*base 0")  # valid in Python 2.7
asserts.fails(lambda: int("-0123", 0), "invalid literal.*base 0")

# github.com/google/starlark-go/issues/108
asserts.fails(lambda: int("0Oxa", 8), "invalid literal for int\\(\\) with base 8: \"0Oxa\"")

# follow-on bugs to issue 108
asserts.fails(lambda: int("--4"), "invalid literal for int\\(\\) with base 10: \"--4\"")
asserts.fails(lambda: int("++4"), "invalid literal for int\\(\\) with base 10: \"\\+\\+4\"")
asserts.fails(lambda: int("+-4"), "invalid literal for int\\(\\) with base 10: \"\\+-4\"")
asserts.fails(lambda: int("0x-4", 16), "invalid literal for int\\(\\) with base 16: \"0x-4\"")

# bitwise union (int|int), intersection (int&int), XOR (int^int), unary not (~int),
# left shift (int<<int), and right shift (int>>int).
# TODO(adonovan): this is not yet in the Starlark spec,
# but there is consensus that it should be.
asserts.eq(1 | 2, 3)
asserts.eq(3 | 6, 7)
asserts.eq((1 | 2) & (2 | 4), 2)
asserts.eq(1 ^ 2, 3)
asserts.eq(2 ^ 2, 0)
asserts.eq(1 | 0 ^ 1, 1)  # check | and ^ operators precedence
asserts.eq(~1, -2)
asserts.eq(~(-2), 1)
asserts.eq(~0, -1)
asserts.eq(1 << 2, 4)
asserts.eq(2 >> 1, 1)
asserts.fails(lambda: 2 << -1, "negative shift count")
asserts.fails(lambda: 1 << 512, "shift count too large")

# comparisons
# TODO(adonovan): test: < > == != etc
def comparisons():
    for m in [1, maxint32 / 2, maxint32]:  # Test small/big ranges
        asserts.lt(-2 * m, -1 * m)
        asserts.lt(-1 * m, 0 * m)
        asserts.lt(0 * m, 1 * m)
        asserts.lt(1 * m, 2 * m)
        asserts.true(2 * m >= 2 * m)
        asserts.true(2 * m > 1 * m)
        asserts.true(1 * m >= 1 * m)
        asserts.true(1 * m > 0 * m)
        asserts.true(0 * m >= 0 * m)
        asserts.true(0 * m > -1 * m)
        asserts.true(-1 * m >= -1 * m)
        asserts.true(-1 * m > -2 * m)

comparisons()

# precision
asserts.eq(str(maxint64), "9223372036854775807")
asserts.eq(str(maxint64 + 1), "9223372036854775808")
asserts.eq(str(minint64), "-9223372036854775808")
asserts.eq(str(minint64 - 1), "-9223372036854775809")
asserts.eq(str(minint64 * minint64), "85070591730234615865843651857942052864")
asserts.eq(str(maxint32 + 1), "2147483648")
asserts.eq(str(minint32 - 1), "-2147483649")
asserts.eq(str(minint32 * minint32), "4611686018427387904")
asserts.eq(str(minint32 | maxint32), "-1")
asserts.eq(str(minint32 & minint32), "-2147483648")
asserts.eq(str(minint32 ^ maxint32), "-1")
asserts.eq(str(minint32 // -1), "2147483648")

# string formatting
asserts.eq("%o %x %d" % (0o755, 0xDEADBEEF, 42), "755 deadbeef 42")
nums = [-95, -1, 0, +1, +95]
asserts.eq(" ".join(["%o" % x for x in nums]), "-137 -1 0 1 137")
asserts.eq(" ".join(["%d" % x for x in nums]), "-95 -1 0 1 95")
asserts.eq(" ".join(["%i" % x for x in nums]), "-95 -1 0 1 95")
asserts.eq(" ".join(["%x" % x for x in nums]), "-5f -1 0 1 5f")
asserts.eq(" ".join(["%X" % x for x in nums]), "-5F -1 0 1 5F")
asserts.eq("%o %x %d" % (123, 123, 123), "173 7b 123")
asserts.eq("%o %x %d" % (123.1, 123.1, 123.1), "173 7b 123")  # non-int operands are acceptable
asserts.fails(lambda: "%d" % True, "%d format requires integer: bool")
