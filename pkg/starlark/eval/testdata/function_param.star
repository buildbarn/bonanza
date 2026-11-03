# Derived from go.starlark.net's starlark/testdata, which is
# Copyright 2017 The Bazel Authors. All rights reserved. Use of this
# source code is governed by the BSD-style license that can be found in
# LICENSE.go-starlark. Modifications have been made to match the
# behavior of Bazel.

# Functions used in starlark_test.TestParamDefault().

def all_required(a, b, c): pass
def all_opt(a="a", b=None, c=""): pass
def mix_required_opt(a, b, c="c", d="d"): pass
def with_varargs(a, b="b", *args): pass
def with_varargs_kwonly(a, b="b", *args, c="c", d): pass
def with_kwonly(a, b="b", *, c="c", d): pass
def with_kwargs(a, b="b", c="c", **kwargs): pass
def with_varargs_kwonly_kwargs(a, b="b", *args, c="c", d, e="e", **kwargs): pass
