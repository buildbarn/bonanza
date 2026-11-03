def f(x, y):
    x[0] += 1
    y.field |= {"a": 1}
    a, (b, [c]) = x
    y.other = x[::2]
    z = -a if b else ~c
    return z or a and b
