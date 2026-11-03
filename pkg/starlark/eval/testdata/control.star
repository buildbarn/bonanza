# Derived from go.starlark.net's starlark/testdata, which is
# Copyright 2017 The Bazel Authors. All rights reserved. Use of this
# source code is governed by the BSD-style license that can be found in
# LICENSE.go-starlark. Modifications have been made to match the
# behavior of Bazel.

# Tests of Starlark control flow

load("assert.star", "asserts")

def controlflow():
  # elif
  x = 0
  if True:
    x=1
  elif False:
    asserts.fail("else of true")
  else:
    asserts.fail("else of else of true")
  asserts.true(x)

  x = 0
  if False:
    asserts.fail("then of false")
  elif True:
    x = 1
  else:
    asserts.fail("else of true")
  asserts.true(x)

  x = 0
  if False:
    asserts.fail("then of false")
  elif False:
    asserts.fail("then of false")
  else:
    x = 1
  asserts.true(x)
controlflow()

def loops():
  y = ""
  for x in [1, 2, 3, 4, 5]:
    if x == 2:
      continue
    if x == 4:
      break
    y = y + str(x)
  return y
asserts.eq(loops(), "13")

# return
g = 123
def f(x):
  for g in (1, 2, 3):
    if g == x:
      return g
asserts.eq(f(2), 2)
asserts.eq(f(4), None) # falling off end => return None
asserts.eq(g, 123) # unchanged by local use of g in function

