load("@bazel_skylib//rules:common_settings.bzl", "BuildSettingInfo")

def _default_make_variables_impl(ctx):
    return [platform_common.TemplateVariableInfo({
        "COMPILATION_MODE": ctx.attr._compilation_mode[BuildSettingInfo].value,
        "BINDIR": ctx.bin_dir.path,
        "TARGET_CPU": ctx.attr._cpu[BuildSettingInfo].value,
    })]

default_make_variables = rule(
    _default_make_variables_impl,
    attrs = {
        "_compilation_mode": attr.label(default = "//command_line_option:compilation_mode"),
        "_cpu": attr.label(default = "//command_line_option:cpu"),
    },
    needs = [],
)
