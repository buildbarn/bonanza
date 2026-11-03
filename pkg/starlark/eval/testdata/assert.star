# Equivalent of go.starlark.net's starlarktest/asserts.star.
#
# Predeclared built-ins for this module:
#
# error(msg): report an error without halting execution.
# catch(f): evaluate f() and returns its evaluation error message, if any.
# matches(pattern, str): report whether str matches regular expression pattern.
# module(name, **kwargs): a constructor for a module.
# _freeze(x): freeze the value x and everything reachable from it.
# _floateq(x, y): reports floating point equality (within 1 ULP).

_num = ("float", "int")

def _eq(x, y):
    if x != y:
        if (type(x) == "float" and type(y) in _num or
            type(y) == "float" and type(x) in _num):
            if not _floateq(float(x), float(y)):
                error("floats: %r != %r (delta > 1 ulp)" % (x, y))
        else:
            error("%r != %r" % (x, y))

def _ne(x, y):
    if x == y:
        error("%r == %r" % (x, y))

def _true(cond, msg = "assertion failed"):
    if not cond:
        error(msg)

def _lt(x, y):
    if not (x < y):
        error("%s is not less than %s" % (x, y))

def _contains(x, y):
    if y not in x:
        error("%s does not contain %s" % (x, y))

def _fails(f, pattern):
    "assert_fails asserts that evaluation of f() fails with the specified error."
    msg = catch(f)
    if msg == None:
        error("evaluation succeeded unexpectedly (want error matching %r)" % pattern)
    elif not matches(pattern, msg):
        error("regular expression (%s) did not match error (%s)" % (pattern, msg))

freeze = _freeze

asserts = module(
    "asserts",
    fail = error,
    eq = _eq,
    ne = _ne,
    true = _true,
    lt = _lt,
    contains = _contains,
    fails = _fails,
)
