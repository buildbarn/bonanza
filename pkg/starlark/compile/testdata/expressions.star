def f(x):
    return [
        x not in M,
        "a" + "b" + "c" + x + "d",
        (1,) + (2,),
        0x10000000000000000,
        1.5,
        {"a": 1, x: 2},
        x.method(1, k = 2),
        M(*x),
        M(**x),
        M(1, *x, **x),
        print(x),
        len,
    ]
