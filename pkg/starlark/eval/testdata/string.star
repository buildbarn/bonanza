# Derived from go.starlark.net's starlark/testdata, which is
# Copyright 2017 The Bazel Authors. All rights reserved. Use of this
# source code is governed by the BSD-style license that can be found in
# LICENSE.go-starlark. Modifications have been made to match the
# behavior of Bazel.

# Tests of Starlark 'string'
# option:set

load("assert.star", "asserts")

# raw string literals:
asserts.eq(r"a\bc", "a\\bc")

# truth
asserts.true("abc")
asserts.true("\000")
asserts.true(not "")

# str + str
asserts.eq("a" + "b" + "c", "abc")

# str * int,  int * str
asserts.eq("abc" * 0, "")
asserts.eq("abc" * -1, "")
asserts.eq("abc" * 1, "abc")
asserts.eq("abc" * 5, "abcabcabcabcabc")
asserts.eq(0 * "abc", "")
asserts.eq(-1 * "abc", "")
asserts.eq(1 * "abc", "abc")
asserts.eq(5 * "abc", "abcabcabcabcabc")
asserts.fails(lambda: 1.0 * "abc", "unsupported binary operation: float \\* string")
asserts.fails(lambda: "abc" * (1000000 * 1000000), "repeat count too large")
asserts.fails(lambda: "abc" * 1000000 * 1000000, "repeated string too large")

# len
asserts.eq(len("Hello, 世界!"), 14)
asserts.eq(len("𐐷"), 4)  # U+10437 has a 4-byte UTF-8 encoding (and a 2-code UTF-16 encoding)

# string.elems (1-byte substrings, which are invalid text). Bytes that
# are not part of valid UTF-8 sequences are printed as octal escapes,
# as Bazel does not support \x escapes.
asserts.eq(type("abcЙ😿".elems()), "list")
asserts.eq(
    repr(list("abcЙ😿".elems())),
    r'["a", "b", "c", "\320", "\231", "\360", "\237", "\230", "\277"]',
)
asserts.eq(
    repr(list(("A" + "😿Z"[1:]).elems())),
    r'["A", "\237", "\230", "\277", "Z"]',
)
asserts.eq(list("".elems()), [])
asserts.eq("abcЙ😿".elems()[2], "c")  # indexable
asserts.eq(len("abcЙ😿".elems()), 9)  # known length

# indexing, x[i]
asserts.eq("Hello, 世界!"[0], "H")
asserts.eq(repr("Hello, 世界!"[7]), r'"\344"')  # (invalid text)
asserts.eq("Hello, 世界!"[13], "!")
asserts.fails(lambda: "abc"[-4], "out of range")
asserts.eq("abc"[-3], "a")
asserts.eq("abc"[-2], "b")
asserts.eq("abc"[-1], "c")
asserts.eq("abc"[0], "a")
asserts.eq("abc"[1], "b")
asserts.eq("abc"[2], "c")
asserts.fails(lambda: "abc"[4], "out of range")

# x[i] = ...
def f():
    "abc"[1] = "B"

asserts.fails(f, "can only assign an element in a dictionary or a list, not in a 'string'")

# slicing, x[i:j]
asserts.eq("abc"[:], "abc")
asserts.eq("abc"[-4:], "abc")
asserts.eq("abc"[-3:], "abc")
asserts.eq("abc"[-2:], "bc")
asserts.eq("abc"[-1:], "c")
asserts.eq("abc"[0:], "abc")
asserts.eq("abc"[1:], "bc")
asserts.eq("abc"[2:], "c")
asserts.eq("abc"[3:], "")
asserts.eq("abc"[4:], "")
asserts.eq("abc"[:-4], "")
asserts.eq("abc"[:-3], "")
asserts.eq("abc"[:-2], "a")
asserts.eq("abc"[:-1], "ab")
asserts.eq("abc"[:0], "")
asserts.eq("abc"[:1], "a")
asserts.eq("abc"[:2], "ab")
asserts.eq("abc"[:3], "abc")
asserts.eq("abc"[:4], "abc")
asserts.eq("abc"[1:2], "b")
asserts.eq("abc"[2:1], "")
asserts.eq(repr("😿"[:1]), r'"\360"')  # (invalid text)

# non-unit strides
asserts.eq("abcd"[0:4:1], "abcd")
asserts.eq("abcd"[::2], "ac")
asserts.eq("abcd"[1::2], "bd")
asserts.eq("abcd"[4:0:-1], "dcb")
asserts.eq("banana"[7::-2], "aaa")
asserts.eq("banana"[6::-2], "aaa")
asserts.eq("banana"[5::-2], "aaa")
asserts.eq("banana"[4::-2], "nnb")
asserts.eq("banana"[::-1], "ananab")
asserts.eq("banana"[None:None:-2], "aaa")
asserts.fails(lambda: "banana"[1.0::], "invalid slice index: got float, want int")
asserts.fails(lambda: "banana"[:"":], "invalid slice index: got string, want int")
asserts.fails(lambda: "banana"[:"":True], "invalid slice step: got bool, want int")

# in, not in
asserts.true("oo" in "food")
asserts.true("ox" not in "food")
asserts.true("" in "food")
asserts.true("" in "")
asserts.fails(lambda: 1 in "", "requires string as left operand")
asserts.fails(lambda: "" in 1, "unsupported binary operation: string in int")

# ==, !=
asserts.eq("hello", "he" + "llo")
asserts.ne("hello", "Hello")

# hash must follow java.lang.String.hashCode. As in Bazel, strings are
# sequences of bytes, so non-ASCII characters hash differently than in
# Java.
wanthash = {
    "": 0,
    "\0" * 100: 0,
    "hello": 99162322,
    "world": 113318802,
    "Hello, 世界!": 1928703871,
}
gothash = {s: hash(s) for s in wanthash}
asserts.eq(gothash, wanthash)

# TODO(adonovan): ordered comparisons

# string % tuple formatting
asserts.eq("A" % (), "A")
asserts.eq("A %d %x Z" % (123, 456), "A 123 1c8 Z")
asserts.eq("A" % {'unused': 123}, "A")
asserts.eq("A %(foo)d %(bar)s Z" % {"foo": 123, "bar": "hi"}, "A 123 hi Z")
asserts.eq("%s %r" % ("hi", "hi"), 'hi "hi"')  # TODO(adonovan): use ''-quotation
asserts.eq("%%d %d" % 1, "%d 1")
asserts.fails(lambda: "%d %d" % 1, "not enough arguments for format string")
asserts.fails(lambda: "%d %d" % (1, 2, 3), "too many arguments for format string")
asserts.fails(lambda: "" % 1, "too many arguments for format string")

# TODO(adonovan): more tests

# str.format
asserts.eq("a{}b".format(123), "a123b")
asserts.eq("a{}b{}c{}d{}".format(1, 2, 3, 4), "a1b2c3d4")
asserts.eq("a{{b".format(), "a{b")
asserts.eq("a}}b".format(), "a}b")
asserts.eq("a{{b}}c".format(), "a{b}c")
asserts.eq("a{x}b{y}c{}".format(1, x = 2, y = 3), "a2b3c1")
asserts.fails(lambda: "a{z}b".format(x = 1), "missing argument 'z'")
asserts.fails(lambda: "{-1}".format(1), "missing argument '-1'")
asserts.fails(lambda: "{-0}".format(1), "missing argument '-0'")
asserts.fails(lambda: "{+0}".format(1), "missing argument '\\+0'")
asserts.fails(lambda: "{+1}".format(1), "missing argument '\\+1'")  # starlark-go/issues/114
asserts.eq("{0000000000001}".format(0, 1), "1")
asserts.eq("{012}".format(*range(100)), "12")  # decimal, despite leading zeros
asserts.fails(lambda: "{0,1} and {1}".format(1, 2), "missing argument '0,1'")
asserts.fails(lambda: "a{123}b".format(), "no replacement found for index 123")
asserts.fails(lambda: "a{}b{}c".format(1), "no replacement found for index 1")
asserts.eq("a{010}b".format(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10), "a10b")  # index is decimal
asserts.fails(lambda: "a{}b{1}c".format(1, 2), "cannot switch from automatic field numbering to manual")
asserts.eq("a{!s}c".format("b"), "abc")
asserts.eq("a{!r}c".format("b"), r'a"b"c')
asserts.eq("a{x!r}c".format(x = "b"), r'a"b"c')
asserts.fails(lambda: "{x!}".format(x = 1), "unknown conversion")
asserts.fails(lambda: "{x!:}".format(x = 1), "unknown conversion")
asserts.fails(lambda: "{a.b}".format(1), "unsupported replacement field")
asserts.fails(lambda: "{a[0]}".format(1), "unsupported replacement field")
asserts.fails(lambda: "{ {} }".format(1), "unsupported replacement field")
asserts.fails(lambda: "{{}".format(1), "found '}' without matching '{'")
asserts.fails(lambda: "{}}".format(1), "found '}' without matching '{'")
asserts.fails(lambda: "}}{".format(1), "found '{' without matching '}'")
asserts.fails(lambda: "}{{".format(1), "found '}' without matching '{'")

# str.split, str.rsplit
asserts.eq("a.b.c.d".split("."), ["a", "b", "c", "d"])
asserts.eq("a.b.c.d".rsplit("."), ["a", "b", "c", "d"])
asserts.eq("a.b.c.d".split(".", -1), ["a", "b", "c", "d"])
asserts.eq("a.b.c.d".rsplit(".", -1), ["a", "b", "c", "d"])
asserts.eq("a.b.c.d".split(".", 0), ["a.b.c.d"])
asserts.eq("a.b.c.d".rsplit(".", 0), ["a.b.c.d"])
asserts.eq("a.b.c.d".split(".", 1), ["a", "b.c.d"])
asserts.eq("a.b.c.d".rsplit(".", 1), ["a.b.c", "d"])
asserts.eq("a.b.c.d".split(".", 2), ["a", "b", "c.d"])
asserts.eq("a.b.c.d".rsplit(".", 2), ["a.b", "c", "d"])
asserts.eq("  ".split("."), ["  "])
asserts.eq("  ".rsplit("."), ["  "])

# {,r}split on white space:
asserts.eq(" a bc\n  def \t  ghi".split(), ["a", "bc", "def", "ghi"])
asserts.eq(" a bc\n  def \t  ghi".split(None), ["a", "bc", "def", "ghi"])
asserts.eq(" a bc\n  def \t  ghi".split(None, 0), ["a bc\n  def \t  ghi"])
asserts.eq(" a bc\n  def \t  ghi".rsplit(None, 0), [" a bc\n  def \t  ghi"])
asserts.eq(" a bc\n  def \t  ghi".split(None, 1), ["a", "bc\n  def \t  ghi"])
asserts.eq(" a bc\n  def \t  ghi".rsplit(None, 1), [" a bc\n  def", "ghi"])
asserts.eq(" a bc\n  def \t  ghi".split(None, 2), ["a", "bc", "def \t  ghi"])
asserts.eq(" a bc\n  def \t  ghi".rsplit(None, 2), [" a bc", "def", "ghi"])
asserts.eq(" a bc\n  def \t  ghi".split(None, 3), ["a", "bc", "def", "ghi"])
asserts.eq(" a bc\n  def \t  ghi".rsplit(None, 3), [" a", "bc", "def", "ghi"])
asserts.eq(" a bc\n  def \t  ghi".split(None, 4), ["a", "bc", "def", "ghi"])
asserts.eq(" a bc\n  def \t  ghi".rsplit(None, 4), ["a", "bc", "def", "ghi"])
asserts.eq(" a bc\n  def \t  ghi".rsplit(None, 5), ["a", "bc", "def", "ghi"])

asserts.eq(" a bc\n  def \t  ghi ".split(None, 0), ["a bc\n  def \t  ghi "])
asserts.eq(" a bc\n  def \t  ghi ".rsplit(None, 0), [" a bc\n  def \t  ghi"])
asserts.eq(" a bc\n  def \t  ghi ".split(None, 1), ["a", "bc\n  def \t  ghi "])
asserts.eq(" a bc\n  def \t  ghi ".rsplit(None, 1), [" a bc\n  def", "ghi"])

# Observe the algorithmic difference when splitting on spaces versus other delimiters.
asserts.eq("--aa--bb--cc--".split("-", 0), ["--aa--bb--cc--"])  # contrast this
asserts.eq("  aa  bb  cc  ".split(None, 0), ["aa  bb  cc  "])  #  with this
asserts.eq("--aa--bb--cc--".rsplit("-", 0), ["--aa--bb--cc--"])  # ditto this
asserts.eq("  aa  bb  cc  ".rsplit(None, 0), ["  aa  bb  cc"])  #  and this

#
asserts.eq("--aa--bb--cc--".split("-", 1), ["", "-aa--bb--cc--"])
asserts.eq("--aa--bb--cc--".rsplit("-", 1), ["--aa--bb--cc-", ""])
asserts.eq("  aa  bb  cc  ".split(None, 1), ["aa", "bb  cc  "])
asserts.eq("  aa  bb  cc  ".rsplit(None, 1), ["  aa  bb", "cc"])

#
asserts.eq("--aa--bb--cc--".split("-", -1), ["", "", "aa", "", "bb", "", "cc", "", ""])
asserts.eq("--aa--bb--cc--".rsplit("-", -1), ["", "", "aa", "", "bb", "", "cc", "", ""])
asserts.eq("  aa  bb  cc  ".split(None, -1), ["aa", "bb", "cc"])
asserts.eq("  aa  bb  cc  ".rsplit(None, -1), ["aa", "bb", "cc"])
asserts.eq("  ".split(None), [])
asserts.eq("  ".rsplit(None), [])

asserts.eq("localhost:80".rsplit(":", 1)[-1], "80")

# str.splitlines
asserts.eq("\nabc\ndef".splitlines(), ["", "abc", "def"])
asserts.eq("\nabc\ndef".splitlines(True), ["\n", "abc\n", "def"])
asserts.eq("\nabc\ndef\n".splitlines(), ["", "abc", "def"])
asserts.eq("\nabc\ndef\n".splitlines(True), ["\n", "abc\n", "def\n"])
asserts.eq("".splitlines(), [])  #
asserts.eq("".splitlines(True), [])  #
asserts.eq("a".splitlines(), ["a"])
asserts.eq("a".splitlines(True), ["a"])
asserts.eq("\n".splitlines(), [""])
asserts.eq("\n".splitlines(True), ["\n"])
asserts.eq("a\n".splitlines(), ["a"])
asserts.eq("a\n".splitlines(True), ["a\n"])
asserts.eq("a\n\nb".splitlines(), ["a", "", "b"])
asserts.eq("a\n\nb".splitlines(True), ["a\n", "\n", "b"])
asserts.eq("a\nb\nc".splitlines(), ["a", "b", "c"])
asserts.eq("a\nb\nc".splitlines(True), ["a\n", "b\n", "c"])
asserts.eq("a\nb\nc\n".splitlines(), ["a", "b", "c"])
asserts.eq("a\nb\nc\n".splitlines(True), ["a\n", "b\n", "c\n"])

# str.{,l,r}strip
asserts.eq(" \tfoo\n ".strip(), "foo")
asserts.eq(" \tfoo\n ".lstrip(), "foo\n ")
asserts.eq(" \tfoo\n ".rstrip(), " \tfoo")
# Unlike go.starlark.net, an empty set of characters strips nothing,
# as in Bazel and Python.
asserts.eq(" \tfoo\n ".strip(""), " \tfoo\n ")
asserts.eq(" \tfoo\n ".lstrip(""), " \tfoo\n ")
asserts.eq(" \tfoo\n ".rstrip(""), " \tfoo\n ")
asserts.eq("blah.h".strip("b.h"), "la")
asserts.eq("blah.h".lstrip("b.h"), "lah.h")
asserts.eq("blah.h".rstrip("b.h"), "bla")

# str.count
asserts.eq("banana".count("a"), 3)
asserts.eq("banana".count("a", 2), 2)
asserts.eq("banana".count("a", -4, -2), 1)
asserts.eq("banana".count("a", 1, 4), 2)
asserts.eq("banana".count("a", 0, -100), 0)

# str.{starts,ends}with
asserts.true("foo".endswith("oo"))
asserts.true(not "foo".endswith("x"))
asserts.true("foo".startswith("fo"))
asserts.true(not "foo".startswith("x"))
asserts.fails(lambda: "foo".startswith(1), "got int.*want string")

#
asserts.true("abc".startswith(("a", "A")))
asserts.true("ABC".startswith(("a", "A")))
asserts.true(not "ABC".startswith(("b", "B")))
asserts.fails(lambda: "123".startswith((1, 2)), "got int in tuple, want string")
asserts.fails(lambda: "123".startswith(["3"]), "got list")

#
asserts.true("abc".endswith(("c", "C")))
asserts.true("ABC".endswith(("c", "C")))
asserts.true(not "ABC".endswith(("b", "B")))
asserts.fails(lambda: "123".endswith((1, 2)), "got int in tuple, want string")
asserts.fails(lambda: "123".endswith(["3"]), "got list")

# start/end
asserts.true("abc".startswith("bc", 1))
asserts.true(not "abc".startswith("b", 999))
asserts.true("abc".endswith("ab", None, -1))
asserts.true(not "abc".endswith("b", None, -999))

# str.replace
asserts.eq("banana".replace("a", "o", 1), "bonana")
asserts.eq("banana".replace("a", "o"), "bonono")
# TODO(adonovan): more tests

# str.{,r}find
asserts.eq("foofoo".find("oo"), 1)
asserts.eq("foofoo".find("ox"), -1)
asserts.eq("foofoo".find("oo", 2), 4)
asserts.eq("foofoo".rfind("oo"), 4)
asserts.eq("foofoo".rfind("ox"), -1)
asserts.eq("foofoo".rfind("oo", 1, 4), 1)
asserts.eq("foofoo".find(""), 0)
asserts.eq("foofoo".rfind(""), 6)

# str.{,r}partition
asserts.eq("foo/bar/wiz".partition("/"), ("foo", "/", "bar/wiz"))
asserts.eq("foo/bar/wiz".rpartition("/"), ("foo/bar", "/", "wiz"))
asserts.eq("foo/bar/wiz".partition("."), ("foo/bar/wiz", "", ""))
asserts.eq("foo/bar/wiz".rpartition("."), ("", "", "foo/bar/wiz"))
asserts.fails(lambda: "foo/bar/wiz".partition(""), "empty separator")
asserts.fails(lambda: "foo/bar/wiz".rpartition(""), "empty separator")

asserts.eq("?".join(["foo", "a/b/c.go".rpartition("/")[0]]), "foo?a/b")

# str.is{alpha,...}
def test_predicates():
    predicates = ["alnum", "alpha", "digit", "lower", "space", "title", "upper"]
    table = {
        "Hello, World!": "title",
        "hello, world!": "lower",
        "base64": "alnum lower",
        "HAL-9000": "upper",
        "Catch-22": "title",
        "": "",
        "\n\t\r": "space",
        "abc": "alnum alpha lower",
        "ABC": "alnum alpha upper",
        "123": "alnum digit",
    }
    for str, want in table.items():
        got = " ".join([name for name in predicates if getattr(str, "is" + name)()])
        if got != want:
            asserts.fail("%r matched [%s], want [%s]" % (str, got, want))

test_predicates()

# Strings are not iterable.
# ok
asserts.eq(len("abc"), 3)  # len
asserts.true("a" in "abc")  # str in str
asserts.eq("abc"[1], "b")  # indexing

# not ok
def for_string():
    for x in "abc":
        pass

def args(*args):
    return args

asserts.fails(lambda: args(*"abc"), "must be iterable, not string")  # varargs
asserts.fails(lambda: list("abc"), "type 'string' is not iterable")  # list(str)
asserts.fails(lambda: tuple("abc"), "type 'string' is not iterable")  # tuple(str)
asserts.fails(lambda: set("abc"), "type 'string' is not iterable")  # set(str)
asserts.fails(lambda: set() | "abc", "unsupported binary operation: set | string")  # set union
asserts.fails(lambda: enumerate("ab"), "type 'string' is not iterable")  # enumerate
asserts.fails(lambda: sorted("abc"), "type 'string' is not iterable")  # sorted
asserts.fails(lambda: [].extend("bc"), "type 'string' is not iterable")  # list.extend
asserts.fails(lambda: ",".join("abc"), "type 'string' is not iterable")  # string.join
asserts.fails(lambda: dict(["ab"]), "not iterable")  # dict
asserts.fails(for_string, "type 'string' is not iterable")  # for loop
asserts.fails(lambda: [x for x in "abc"], "type 'string' is not iterable")  # comprehension
asserts.fails(lambda: all("abc"), "type 'string' is not iterable")  # all
asserts.fails(lambda: any("abc"), "type 'string' is not iterable")  # any
asserts.fails(lambda: reversed("abc"), "type 'string' is not iterable")  # reversed
asserts.fails(lambda: zip("ab", "cd"), "type 'string' is not iterable")  # zip

# str.join
asserts.eq(",".join([]), "")
asserts.eq(",".join(["a"]), "a")
asserts.eq(",".join(["a", "b"]), "a,b")
asserts.eq(",".join(["a", "b", "c"]), "a,b,c")
asserts.eq(",".join(("a", "b", "c")), "a,b,c")
asserts.eq("".join(("a", "b", "c")), "abc")
asserts.fails(lambda: "".join(None), "type 'NoneType' is not iterable")
asserts.fails(lambda: "".join(["one", 2]), "expected string for sequence element 1, got 'int'")

# TODO(adonovan): tests for: {,r}index

# str.capitalize
asserts.eq("hElLo, WoRlD!".capitalize(), "Hello, world!")
asserts.eq("por qué".capitalize(), "Por qué")
asserts.eq("¿Por qué?".capitalize(), "¿por qué?")

# str.lower
asserts.eq("hElLo, WoRlD!".lower(), "hello, world!")
asserts.eq("por qué".lower(), "por qué")
asserts.eq("¿Por qué?".lower(), "¿por qué?")

# str.upper
asserts.eq("hElLo, WoRlD!".upper(), "HELLO, WORLD!")
# Case conversion only affects ASCII characters.
asserts.eq("por qué".upper(), "POR QUé")
asserts.eq("¿Por qué?".upper(), "¿POR QUé?")

# str.title
asserts.eq("hElLo, WoRlD!".title(), "Hello, World!")
asserts.eq("por qué".title(), "Por Qué")
asserts.eq("¿Por qué?".title(), "¿Por Qué?")

# method spell check
asserts.fails(lambda: "".starts_with, "has no field or method 'starts_with' \\(did you mean 'startswith'\\?\\)")
asserts.fails(lambda: "".StartsWith, "has no field or method 'StartsWith' \\(did you mean 'startswith'\\?\\)")
asserts.fails(lambda: "".fin, "has no field or method 'fin' \\(did you mean 'find'\\?\\)")


# removesuffix
asserts.eq("Apricot".removesuffix("cot"), "Apri")
asserts.eq("Apricot".removesuffix("Cot"), "Apricot")
asserts.eq("Apricot".removesuffix("t"), "Aprico")
asserts.eq("a".removesuffix(""), "a")
asserts.eq("".removesuffix(""), "")
asserts.eq("".removesuffix("a"), "")
asserts.eq("Apricot".removesuffix("co"), "Apricot")
asserts.eq("Apricotcot".removesuffix("cot"), "Apricot")

# removeprefix
asserts.eq("Apricot".removeprefix("Apr"), "icot")
asserts.eq("Apricot".removeprefix("apr"), "Apricot")
asserts.eq("Apricot".removeprefix("A"), "pricot")
asserts.eq("a".removeprefix(""), "a")
asserts.eq("".removeprefix(""), "")
asserts.eq("".removeprefix("a"), "")
asserts.eq("Apricot".removeprefix("pr"), "Apricot")
asserts.eq("AprApricot".removeprefix("Apr"), "Apricot")