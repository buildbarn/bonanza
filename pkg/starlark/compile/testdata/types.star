def f(x: int, y: list[str] = []) -> str:
    z: str = cast(str, x)
    w: int
    return z

type T = int | str
