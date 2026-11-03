# option:build
M(name = "a")

load("//a:a.bzl", "b", c = "d")

b(name = c)
