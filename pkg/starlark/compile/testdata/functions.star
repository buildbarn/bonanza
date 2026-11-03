load("//a:a.bzl", "a")

x = [1, 2] + [3]

def f(p, q = 1, *args, k, **kwargs):
    """Docstring."""
    total = 0
    for i in p:
        if i > 1 and not q:
            continue
        total += i
    return total + a + x

y = {k: v for k, v in M.items() if v}
