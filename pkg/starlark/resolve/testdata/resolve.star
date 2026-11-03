# Tests of resolver errors.
#
# Many of these tests were derived from go.starlark.net's
# resolve/testdata/resolve.star, which is Copyright 2017 The Bazel
# Authors and governed by a BSD-style license. The expected errors
# follow the behavior of Bazel.
#
# Chunks are separated by "---". Each line may contain one or more
# expected errors, written as three hashes followed by one or more
# quoted regular expressions. Unless options say otherwise, chunks are
# resolved using the dialect of .bzl files. The initial environment
# contains the predeclared name "M" and the universal names "U",
# "None", "int", "len" and "print".

# use of declared global
x = 1
_ = x

---
# premature use of global is not a static error
_ = x
x = 1

---
# use of undefined global
_ = x ### "name 'x' is not defined"

---
# redeclaration of global
x = 1 ### "'x' previously declared here"
x = 2 ### "'x' redeclared at top level"

---
# Redeclaration of predeclared names is permitted once.
M = 1
U = 1

---
# A global declaration shadows all references to a predeclared name,
# even those preceding it.
a = U
U = 1

---
# reference to predeclared name
M()

---
# locals may be referenced before they are defined
def f():
   M(x) # dynamic error
   x = 1

---
# Various forms of assignment:
def f(x): # parameter
    M(x)
    M(y) ### "name 'y' is not defined"

(a, b) = 1, 2
M(a)
M(b)
M(c) ### "name 'c' is not defined"

[p, q] = 1, 2
M(p)
M(q)
M(r) ### "name 'r' is not defined"

---
# a comprehension introduces a separate lexical block
_ = [x for x in "abc"]
M(x) ### "name 'x' is not defined"

---
# Comprehension variables may be referred to by earlier clauses.
_ = [x for y in M if x for x in y]

---
# The iterable of the first for clause is resolved in the enclosing
# block, while the others are resolved in the comprehension.
a = [x for x in x] ### "name 'x' is not defined"
b = [y for x in M for y in x]

---
# Functions may have forward refs.
def f():
   g()
   h() ### "name 'h' is not defined"
   def inner():
     i()
     i = lambda: 0

def g():
  f()

---
# It is not permitted to rebind a global using a += assignment.
x = [1] ### "'x' previously declared here"
x.extend([2]) # ok
x += [3] ### "'x' redeclared at top level"

def f():
   x += [4] # x is local to f

---
def f(a):
  if 1==1:
    b = 1
  c = 1
  M(a) # ok: param
  M(b) # ok: maybe bound local
  M(c) # ok: bound local
  M(d) # NB: we don't do a use-before-def check on local vars!
  M(e) # ok: global
  M(f) # ok: global
  d = 1

e = 1

---
load("module", "name") # ok
load("foo",
     "",     ### `load: "" is not a valid identifier`
     "_a",   ### "symbol '_a' is private and cannot be imported"
     b="",   ### `load: "" is not a valid identifier`
     c="_d", ### "symbol '_d' is private and cannot be imported"
     _e="f") # ok

def f():
  load("foo", "bar") ### "load statement not at top level"

---
load("foo", "a", "a") ### "load statement defines 'a' more than once"

---
# Load statements must come first, except for docstrings.
"""Docstring."""

load("foo", "a") # ok
x = 1
load("foo", "b") ### "load statements must appear before any other statement \(first non-load statement at line 5\)"
load("foo", "c") # only reported once

---
# option:loadsanywhere
x = 1
load("foo", "a") # ok

---
# return statements must be within a function
return ### "return statements must be inside a function"

---
# if statements and for loops at the top level are forbidden
for x in "abc": ### "for loops are not allowed at the top level"
  pass

if x: ### "if statements are not allowed at the top level"
  pass

---
def f():
  for x in "abc": # ok
    pass
  if x: # ok
    pass

---
# The parser allows any expression on the LHS of an assignment.
1 = 0 ### "cannot assign to a literal"
1+2 = 0 ### "cannot assign to an operator expression"
f() = 0 ### "cannot assign to a function call"

[a, b] = 0
[c, d] += 0 ### "cannot perform augmented assignment on a list or tuple expression"
(e, f) += 0 ### "cannot perform augmented assignment on a list or tuple expression"

[] = 0 # ok
() = 0 # ok

---
# break and continue statements must appear within a loop
break ### "break statement must be inside a for loop"

continue ### "continue statement must be inside a for loop"

pass

def f():
  for x in U:
    def g():
      break ### "break statement must be inside a for loop"

---
# Positional arguments (and required parameters) must appear before
# named arguments (and optional parameters).
M(x=1, 2) ### "positional argument may not follow keyword argument"

def f(x=1, y): pass ### "required positional parameter y may not follow an optional parameter"

def g(x=1, *, y): pass # ok: y is keyword-only

---
# No parameters may follow **kwargs in a declaration.
def f(**kwargs, x): ### "required parameter x may not follow \*\*kwargs"
  pass

def g(**kwargs, *args): ### `\* parameter may not follow \*\*kwargs`
  pass

def h(**kwargs1, **kwargs2): ### `multiple \*\* parameters not allowed`
  pass

def i(**kwargs, x=1): ### `optional parameter may not follow \*\*kwargs`
  pass

---
# Only keyword-only params and **kwargs may follow *args in a declaration.
def f(*args, x): # ok
  pass

def g(*args1, *args2): ### `multiple \* parameters not allowed`
  pass

def h(*, ### `bare \* must be followed by keyword-only parameters`
      *): ### `multiple \* parameters not allowed`
  pass

def i(*args, *): ### `multiple \* parameters not allowed`
  pass

def k(*, **kwargs): ### `bare \* must be followed by keyword-only parameters`
  pass

def l(*): ### `bare \* must be followed by keyword-only parameters`
  pass

def m(*args, a=1, **kwargs): # ok
  pass

def n(*, a=1, **kwargs): # ok
  pass

---
# No arguments may follow **kwargs in a call.
def f(*args, **kwargs):
  pass

f(**{}, 1) ### `positional argument may not follow \*\*kwargs`
f(**{}, x=1) ### `keyword argument x may not follow \*\*kwargs`
f(**{}, *[]) ### `\*args may not follow \*\*kwargs`
f(**{}, **{}) ### `multiple \*\*kwargs not allowed`

---
# Only **kwargs may follow *args in a call.
def f(*args, **kwargs):
  pass

f(*[], 1) ### `positional argument may not follow \*args`
f(*[], a=1) ### `keyword argument a may not follow \*args`
f(*[], *[]) ### `multiple \*args not allowed`
f(*[], **{}) # ok
f(a=1, *[]) # ok

---
# Parameter names must be unique.
def f(a, b, a): pass ### "duplicate parameter: a"
def g(args, b, *args): pass ### "duplicate parameter: args"
def h(kwargs, a, **kwargs): pass ### "duplicate parameter: kwargs"
def i(*x, **x): pass ### "duplicate parameter: x"

---
def f(**kwargs): pass
f(a=1, a=1) ### "duplicate keyword argument: a"

---
# spelling
hello = 1
print(hollo) ### `name 'hollo' is not defined \(did you mean 'hello'\?\)`

def f(abc):
   print(abd) ### `name 'abd' is not defined \(did you mean 'abc'\?\)`
   print(goodbye) ### `name 'goodbye' is not defined$`

---
load("module", "x") ### "'x' previously declared as file-local here"
x = 1 ### "conflicting global declaration of 'x'"

---
load("module", "x") ### "'x' previously declared here"
load("module", "x") ### "'x' redeclared at top level"

---
# option:rebinding
load("module", "x") ### "'x' previously declared as file-local here"
x = 1 ### "conflicting global declaration of 'x'"

---
# option:rebinding
x = 1
x = 2 # ok

---
# option:build
# Loading the same symbol repeatedly is permitted, and loads may follow
# uses of the name, as symbols are loaded lazily.
_ = x
load("module", "x") # ok
load("module", "x") # ok
load("module", y = "x") # ok

---
# option:build
load("module", "x") ### "'x' previously loaded here"
load("other", "x") ### "'x' is loaded as a different symbol than before"

---
# option:build
load("module", "x") ### "'x' previously loaded here"
load("module", x = "y") ### "'x' is loaded as a different symbol than before"

---
# option:build
load("module", "x") ### "'x' previously declared as file-local here"
x = 1 ### "conflicting global declaration of 'x'"

---
# option:build
x = 1 ### "'x' previously declared as global here"
load("module", "x") ### "conflicting file-local declaration of 'x'"

---
# forward reference to file-local
_ = x
load("module", "x") ### "load statements must appear before any other statement"

---
# option:build
load("foo", "bar") # ok

def f(): ### "functions may not be defined in BUILD files. You may move the function to a .bzl file and load it."
  pass

g = lambda: 0 ### "functions may not be defined in BUILD files"

for x in U: ### "`for` statements are not allowed in BUILD files. You may inline the loop, move it to a function definition \(in a .bzl file\), or as a last resort use a list comprehension."
  pass

if U: ### "`if` statements are not allowed in BUILD files. You may move conditional logic to a function definition \(in a .bzl file\), or use an `if` expression for simple cases."
  pass

M(*U) ### "\*args arguments are not allowed in BUILD files. Pass the arguments in explicitly."
M(**{}) ### "\*\*kwargs arguments are not allowed in BUILD files. Pass the arguments in explicitly."
M(**U) ### "\*\*kwargs arguments are not allowed in BUILD files" "\*\*kwargs arguments must be a literal dict in BUILD files."

# Names may be reassigned, and loads may follow other statements.
x = 1
x = 2
load("foo", "bar")

---
# option:module
load("foo", "bar") ### "`load` statements may not be used in MODULE.bazel files"

def f(): ### "functions may not be defined in MODULE.bazel files.$"
  pass

for x in U: ### "`for` statements are not allowed in MODULE.bazel files. You may inline the loop or as a last resort use a list comprehension."
  pass

if U: ### "`if` statements are not allowed in MODULE.bazel files. You may use an `if` expression for simple cases."
  pass

M(**{}) # ok
M(**U) ### "\*\*kwargs arguments must be a literal dict in MODULE.bazel files."

---
# option:types
def f(x: int, y: Undefined) -> list[U]: ### "name 'Undefined' is not defined" "name 'list' is not defined"
  z: int = x
  return z

type T = int | M
type V = T

def g[X](x: X) -> X:
  return x

w: X ### "name 'X' is not defined"

def h():
  type Z = int ### "type alias statement not at top level"

a = isinstance(U, M) ### "isinstance\(\) is not yet supported"
b = cast(T, U)

---
# Without type resolution, names in type annotations are not checked,
# and type aliases do not bind names.
def f(x: int, y: Undefined) -> list[U]:
  return x

type T = int
_ = T ### "name 'T' is not defined"
