def f(x):
    for i in x:
        for j in i:
            if not j:
                return
            if j == 3:
                break
    return [j for i in x for j in i if j]
