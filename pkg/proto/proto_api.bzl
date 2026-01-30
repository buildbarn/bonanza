load("@aspect_bazel_lib//lib:copy_to_directory.bzl", "copy_to_directory_bin_action")
load("@rules_proto//proto:defs.bzl", "ProtoInfo")

def _complete_proto_api_impl(ctx):
    transitive_sources = depset(
        transitive = [dep[ProtoInfo].transitive_sources for dep in ctx.attr.roots],
    ).to_list()
    out_dir = ctx.actions.declare_directory(ctx.label.name)
    copy_to_directory_bin = ctx.toolchains["@aspect_bazel_lib//lib:copy_to_directory_toolchain_type"].copy_to_directory_info.bin
    copy_to_directory_bin_action(
        ctx,
        name = ctx.label.name,
        dst = out_dir,
        copy_to_directory_bin = copy_to_directory_bin,
        files = transitive_sources,
        include_external_repositories = ["**"],
        root_paths = ["**/_virtual_imports/*"],
    )

    return [DefaultInfo(files = depset([out_dir]))]

complete_proto_api = rule(
    implementation = _complete_proto_api_impl,
    attrs = {
        "roots": attr.label_list(
            providers = [ProtoInfo],
        ),
    },
    toolchains = ["@aspect_bazel_lib//lib:copy_to_directory_toolchain_type"],
    doc = "Collects transitive .proto sources into a directory with import-path layout.",
)
