def outer(a):
    b = 1

    def middle():
        def inner():
            return a + b
        return inner

    adder = lambda x, y = b: x + y + a
    return middle, adder
