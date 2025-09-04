load("@bazel_skylib//rules:common_settings.bzl", "BuildSettingInfo")
load(":fragment_info.bzl", "FragmentInfo")

def _apple_fragment_impl(ctx):
    return [FragmentInfo(
        include_xcode_exec_requirements = ctx.attr._include_xcode_execution_requirements[BuildSettingInfo].value,
        ios_minimum_os_flag = ctx.attr._ios_minimum_os[BuildSettingInfo].value,
        ios_sdk_version_flag = ctx.attr._ios_sdk_version[BuildSettingInfo].value,
        macos_minimum_os_flag = ctx.attr._macos_minimum_os[BuildSettingInfo].value,
        macos_sdk_version_flag = ctx.attr._macos_sdk_version[BuildSettingInfo].value,
        prefer_mutual_xcode = ctx.attr._prefer_mutual_xcode[BuildSettingInfo].value,
        single_arch_platform = struct(
            platform_type = ctx.attr._apple_platform_type[BuildSettingInfo].value,
        ),
        tvos_minimum_os_flag = ctx.attr._tvos_minimum_os[BuildSettingInfo].value,
        tvos_sdk_version_flag = ctx.attr._tvos_sdk_version[BuildSettingInfo].value,
        watchos_minimum_os_flag = ctx.attr._watchos_minimum_os[BuildSettingInfo].value,
        watchos_sdk_version_flag = ctx.attr._watchos_sdk_version[BuildSettingInfo].value,
        xcode_version_flag = ctx.attr._xcode_version[BuildSettingInfo].value,
    )]

apple_fragment = rule(
    _apple_fragment_impl,
    attrs = {
        "_apple_platform_type": attr.label(default = "//command_line_option:apple_platform_type"),
        "_include_xcode_execution_requirements": attr.label(default = "//command_line_option:experimental_include_xcode_execution_requirements"),
        "_ios_minimum_os": attr.label(default = "//command_line_option:ios_minimum_os"),
        "_ios_sdk_version": attr.label(default = "//command_line_option:ios_sdk_version"),
        "_macos_minimum_os": attr.label(default = "//command_line_option:macos_minimum_os"),
        "_macos_sdk_version": attr.label(default = "//command_line_option:macos_sdk_version"),
        "_prefer_mutual_xcode": attr.label(default = "//command_line_option:experimental_prefer_mutual_xcode"),
        "_tvos_minimum_os": attr.label(default = "//command_line_option:tvos_minimum_os"),
        "_tvos_sdk_version": attr.label(default = "//command_line_option:tvos_sdk_version"),
        "_watchos_minimum_os": attr.label(default = "//command_line_option:watchos_minimum_os"),
        "_watchos_sdk_version": attr.label(default = "//command_line_option:watchos_sdk_version"),
        "_xcode_version": attr.label(default = "//command_line_option:xcode_version"),
    },
    needs = [],
)

def _bazel_py_fragment_impl(ctx):
    return [FragmentInfo(
        python_import_all_repositories = ctx.attr._python_import_all_repositories[BuildSettingInfo].value,
        python_path = ctx.attr._python_path[BuildSettingInfo].value,
    )]

bazel_py_fragment = rule(
    _bazel_py_fragment_impl,
    attrs = {
        "_python_import_all_repositories": attr.label(default = "//command_line_option:experimental_python_import_all_repositories"),
        "_python_path": attr.label(default = "//command_line_option:python_path"),
    },
    needs = [],
)

def _configuration_fragment_impl(ctx):
    has_separate_genfiles_directory = not ctx.attr._merge_genfiles_directory[BuildSettingInfo].value
    is_exec_configuration = ctx.attr._is_exec_configuration[BuildSettingInfo].value
    stamp = ctx.attr._stamp[BuildSettingInfo].value
    return [FragmentInfo(
        # TODO: Fill this in properly!
        coverage_enabled = False,
        # TODO: What needs to go here?
        default_shell_env = {},
        has_separate_genfiles_directory = lambda: has_separate_genfiles_directory,
        # TODO: Have a helper rule that checks whether the exec platform
        # is Windows.
        host_path_separator = ":",
        is_sibling_repository_layout = lambda: False,
        is_tool_configuration = lambda: is_exec_configuration,
        stamp_binaries = lambda: stamp,
    )]

configuration_fragment = rule(
    _configuration_fragment_impl,
    attrs = {
        "_is_exec_configuration": attr.label(default = "//command_line_option:is exec configuration"),
        "_merge_genfiles_directory": attr.label(default = "//command_line_option:incompatible_merge_genfiles_directory"),
        "_stamp": attr.label(default = "//command_line_option:stamp"),
    },
    needs = [],
)

def _cpp_fragment_impl(ctx):
    compilation_mode = ctx.attr._compilation_mode[BuildSettingInfo].value
    cs_fdo_instrument = ctx.attr._cs_fdo_instrument[BuildSettingInfo].value
    dynamic_mode = ctx.attr._dynamic_mode[BuildSettingInfo].value.upper()
    experimental_cc_implementation_deps = ctx.attr._cc_implementation_deps[BuildSettingInfo].value
    experimental_starlark_compiling = ctx.attr._starlark_compiling[BuildSettingInfo].value
    experimental_starlark_linking = ctx.attr._starlark_linking[BuildSettingInfo].value
    fdo_instrument = ctx.attr._fdo_instrument[BuildSettingInfo].value
    fission = ctx.attr._fission[BuildSettingInfo].value
    fission_active_for_current_compilation_mode = (
        True if fission == "yes" else False if fission == "no" else compilation_mode in fission.split(",")
    )
    force_pic = ctx.attr._force_pic[BuildSettingInfo].value
    generate_llvm_lcov = ctx.attr._generate_llvm_lcov[BuildSettingInfo].value
    grte_top = ctx.attr._grte_top.label if ctx.attr._grte_top else None
    minimum_os_version = ctx.attr._minimum_os_version[BuildSettingInfo].value
    process_headers_in_dependencies = ctx.attr._process_headers_in_dependencies[BuildSettingInfo].value
    propeller_optimize_absolute_cc_profile = ctx.attr._propeller_optimize_absolute_cc_profile[BuildSettingInfo].value
    propeller_optimize_absolute_ld_profile = ctx.attr._propeller_optimize_absolute_ld_profile[BuildSettingInfo].value
    proto_profile = ctx.attr._proto_profile[BuildSettingInfo].value
    remove_legacy_whole_archive = ctx.attr._remove_legacy_whole_archive[BuildSettingInfo].value
    save_feature_state = ctx.attr._save_feature_state[BuildSettingInfo].value
    start_end_lib = ctx.attr._start_end_lib[BuildSettingInfo].value
    strip = ctx.attr._strip[BuildSettingInfo].value
    stripopt = ctx.attr._stripopt[BuildSettingInfo].value
    should_strip_binaries = strip == "always" or (strip == "sometimes" and compilation_mode == "fastbuild")
    use_specific_tool_files = ctx.attr._use_specific_tool_files[BuildSettingInfo].value
    return [FragmentInfo(
        apple_generate_dsym = ctx.attr._apple_generate_dsym[BuildSettingInfo].value,
        collect_code_coverage = ctx.attr._collect_code_coverage[BuildSettingInfo].value,
        compilation_mode = lambda: compilation_mode,
        conlyopts = ctx.attr._conlyopt[BuildSettingInfo].value,
        copts = ctx.attr._copt[BuildSettingInfo].value,
        cs_fdo_instrument = lambda: cs_fdo_instrument,
        cs_fdo_path = lambda: None,  # We assume --cs_fdo_optimize is always a label.
        custom_malloc = ctx.attr._custom_malloc[BuildSettingInfo].value if ctx.attr._custom_malloc else None,
        cxxopts = ctx.attr._cxxopt[BuildSettingInfo].value,
        do_not_use_macos_set_install_name = ctx.attr._macos_set_install_name[BuildSettingInfo].value,
        dont_enable_host_nonhost = ctx.attr._dont_enable_host_nonhost_crosstool_features[BuildSettingInfo].value,
        dynamic_mode = lambda: dynamic_mode,
        experimental_cc_implementation_deps = lambda: experimental_cc_implementation_deps,
        experimental_starlark_compiling = lambda: experimental_starlark_compiling,
        experimental_starlark_linking = lambda: experimental_starlark_linking,
        fdo_instrument = lambda: fdo_instrument,
        fdo_path = lambda: None,  # We assume --fdo_optimize is always a label.
        fdo_prefetch_hints = ctx.attr._fdo_prefetch_hints.label if ctx.attr._fdo_prefetch_hints else None,
        fission_active_for_current_compilation_mode = lambda: fission_active_for_current_compilation_mode,
        force_pic = lambda: force_pic,
        generate_llvm_lcov = lambda: generate_llvm_lcov,
        grte_top = lambda: grte_top,
        incompatible_remove_legacy_whole_archive = lambda: remove_legacy_whole_archive,
        incompatible_use_specific_tool_files = lambda: use_specific_tool_files,
        linkopts = ctx.attr._linkopt[BuildSettingInfo].value,
        minimum_os_version = lambda: minimum_os_version,
        process_headers_in_dependencies = lambda: process_headers_in_dependencies,
        propeller_optimize_absolute_cc_profile = lambda: propeller_optimize_absolute_cc_profile,
        propeller_optimize_absolute_ld_profile = lambda: propeller_optimize_absolute_ld_profile,
        proto_profile = lambda: proto_profile,
        save_feature_state = lambda: save_feature_state,
        should_strip_binaries = lambda: should_strip_binaries,
        start_end_lib = lambda: start_end_lib,
        strip_opts = lambda: stripopt,
    )]

cpp_fragment = rule(
    _cpp_fragment_impl,
    attrs = {
        "_apple_generate_dsym": attr.label(default = "//command_line_option:apple_generate_dsym"),
        "_cc_implementation_deps": attr.label(default = "//command_line_option:experimental_cc_implementation_deps"),
        "_collect_code_coverage": attr.label(default = "//command_line_option:collect_code_coverage"),
        "_compilation_mode": attr.label(default = "//command_line_option:compilation_mode"),
        "_conlyopt": attr.label(default = "//command_line_option:conlyopt"),
        "_copt": attr.label(default = "//command_line_option:copt"),
        "_cs_fdo_instrument": attr.label(default = "//command_line_option:cs_fdo_instrument"),
        "_custom_malloc": attr.label(default = "//command_line_option:custom_malloc"),
        "_cxxopt": attr.label(default = "//command_line_option:cxxopt"),
        "_dont_enable_host_nonhost_crosstool_features": attr.label(default = "//command_line_option:incompatible_dont_enable_host_nonhost_crosstool_features"),
        "_dynamic_mode": attr.label(default = "//command_line_option:dynamic_mode"),
        "_fdo_instrument": attr.label(default = "//command_line_option:fdo_instrument"),
        "_fdo_prefetch_hints": attr.label(default = "//command_line_option:fdo_prefetch_hints"),
        "_fission": attr.label(default = "//command_line_option:fission"),
        "_force_pic": attr.label(default = "//command_line_option:force_pic"),
        "_generate_llvm_lcov": attr.label(default = "//command_line_option:experimental_generate_llvm_lcov"),
        "_grte_top": attr.label(default = "//command_line_option:grte_top"),
        "_linkopt": attr.label(default = "//command_line_option:linkopt"),
        "_macos_set_install_name": attr.label(default = "//command_line_option:incompatible_macos_set_install_name"),
        "_minimum_os_version": attr.label(default = "//command_line_option:minimum_os_version"),
        "_process_headers_in_dependencies": attr.label(default = "//command_line_option:process_headers_in_dependencies"),
        "_propeller_optimize_absolute_cc_profile": attr.label(default = "//command_line_option:propeller_optimize_absolute_cc_profile"),
        "_propeller_optimize_absolute_ld_profile": attr.label(default = "//command_line_option:propeller_optimize_absolute_ld_profile"),
        "_proto_profile": attr.label(default = "//command_line_option:proto_profile"),
        "_remove_legacy_whole_archive": attr.label(default = "//command_line_option:incompatible_remove_legacy_whole_archive"),
        "_save_feature_state": attr.label(default = "//command_line_option:experimental_save_feature_state"),
        "_starlark_compiling": attr.label(default = "//command_line_option:experimental_starlark_compiling"),
        "_starlark_linking": attr.label(default = "//command_line_option:experimental_starlark_linking"),
        "_start_end_lib": attr.label(default = "//command_line_option:start_end_lib"),
        "_strip": attr.label(default = "//command_line_option:strip"),
        "_stripopt": attr.label(default = "//command_line_option:stripopt"),
        "_use_specific_tool_files": attr.label(default = "//command_line_option:incompatible_use_specific_tool_files"),
    },
    needs = [],
)

def _java_fragment_impl(ctx):
    disallow_java_import_empty_jars = ctx.attr._disallow_java_import_empty_jars[BuildSettingInfo].value
    disallow_java_import_exports = ctx.attr._disallow_java_import_exports[BuildSettingInfo].value
    use_ijars = ctx.attr._use_ijars[BuildSettingInfo].value
    return [FragmentInfo(
        disallow_java_import_empty_jars = lambda: disallow_java_import_empty_jars,
        disallow_java_import_exports = lambda: disallow_java_import_exports,
        use_ijars = lambda: use_ijars,
    )]

java_fragment = rule(
    _java_fragment_impl,
    attrs = {
        "_disallow_java_import_empty_jars": attr.label(default = "//command_line_option:incompatible_disallow_java_import_empty_jars"),
        "_disallow_java_import_exports": attr.label(default = "//command_line_option:incompatible_disallow_java_import_exports"),
        "_use_ijars": attr.label(default = "//command_line_option:use_ijars"),
    },
    needs = [],
)

def _platform_fragment_impl(ctx):
    return [FragmentInfo(
        host_platform = ctx.attr._host_platform.label,
        platform = ctx.attr._platform.label,
    )]

platform_fragment = rule(
    _platform_fragment_impl,
    attrs = {
        "_host_platform": attr.label(
            cfg = "exec",
            default = "//command_line_option:platforms",
        ),
        "_platform": attr.label(
            cfg = "target",
            default = "//command_line_option:platforms",
        ),
    },
    needs = ["default_exec_group"],
)

def _proto_fragment_impl(ctx):
    return [FragmentInfo(
        experimental_protoc_opts = ctx.attr._protocopt[BuildSettingInfo].value,
    )]

proto_fragment = rule(
    _proto_fragment_impl,
    attrs = {
        "_protocopt": attr.label(default = "//command_line_option:protocopt"),
    },
    needs = [],
)

def _py_fragment_impl(ctx):
    return [FragmentInfo(
        build_python_zip = ctx.attr._build_python_zip[BuildSettingInfo].value,
        default_to_explicit_init_py = ctx.attr._default_to_explicit_init_py[BuildSettingInfo].value,
        use_toolchains = ctx.attr._use_python_toolchains[BuildSettingInfo].value,
    )]

py_fragment = rule(
    _py_fragment_impl,
    attrs = {
        "_build_python_zip": attr.label(default = "//command_line_option:build_python_zip"),
        "_default_to_explicit_init_py": attr.label(default = "//command_line_option:incompatible_default_to_explicit_init_py"),
        "_use_python_toolchains": attr.label(default = "//command_line_option:incompatible_use_python_toolchains"),
    },
    needs = [],
)
