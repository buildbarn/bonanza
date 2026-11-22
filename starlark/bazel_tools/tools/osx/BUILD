load(
    "//tools/osx:xcode_version_flag.bzl",
    "ios_sdk_version_flag",
    "macos_sdk_version_flag",
    "tvos_sdk_version_flag",
    "visionos_sdk_version_flag",
    "watchos_sdk_version_flag",
    "xcode_version_flag",
)

package(default_visibility = ["//visibility:public"])

xcode_version_flag(name = "xcode_version_flag")

[
    xcode_version_flag(
        name = "xcode_version_flag_{}".format(precision),
        precision = precision,
    )
    for precision in [
        "major",
        "minor",
        "patch",
        "exact",
    ]
]

ios_sdk_version_flag(name = "ios_sdk_version_flag")

tvos_sdk_version_flag(name = "tvos_sdk_version_flag")

visionos_sdk_version_flag(name = "visionos_sdk_version_flag")

watchos_sdk_version_flag(name = "watchos_sdk_version_flag")

macos_sdk_version_flag(name = "macos_sdk_version_flag")
