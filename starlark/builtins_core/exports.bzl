AnalysisFailureInfo = provider()
AnalysisTestResultInfo = provider()
ConfigSettingInfo = provider()
ConstraintSettingInfo = provider()
ConstraintValueInfo = provider()
DeclaredToolchainInfo = provider()
ExecutionInfo = provider()
FeatureFlagInfo = provider()
FilesToRunProvider = provider()
InstrumentedFilesInfo = provider()
JavaPluginInfo = provider()
OutputGroupInfo = provider(dict_like = True)
PackageSpecificationInfo = provider()
PlatformInfo = provider()
SymlinkEntry = provider()
ToolchainInfo = provider()
ToolchainTypeInfo = provider()

def _runfiles_init(*, files = None, root_symlinks = None, symlinks = None):
    return {
        "empty_filenames": depset(),
        "files": files or depset(),
        "root_symlinks": root_symlinks or depset(),
        "symlinks": symlinks or depset(),
    }

def _runfiles_merge(r):
    def merge(other):
        return runfiles(
            files = depset(transitive = [r.files, other.files]),
            root_symlinks = depset(transitive = [r.root_symlinks, other.root_symlinks]),
            symlinks = depset(transitive = [r.symlinks, other.symlinks]),
        )

    return merge

def _runfiles_merge_all(r):
    def merge_all(other):
        if not other:
            return r
        return runfiles(
            files = depset(transitive = [r.files] + [o.files for o in other]),
            root_symlinks = depset(transitive = [r.root_symlinks] + [o.root_symlinks for o in other]),
            symlinks = depset(transitive = [r.symlinks] + [o.symlinks for o in other]),
        )

    return merge_all

runfiles, _runfiles_raw = provider(
    computed_fields = {
        "merge": _runfiles_merge,
        "merge_all": _runfiles_merge_all,
    },
    init = _runfiles_init,
    type_name = "runfiles",
)

_runfiles = runfiles

def _default_info_init(*, data_runfiles = None, default_runfiles = None, executable = None, files = None, runfiles = None):
    # According to the Bazel documentation, only the runfiles parameter
    # should be used. Calling DefaultInfo() with data_runfiles or
    # default_runfiles is deprecated. In this implementation we simply
    # merge all of the runfiles together.
    merged_runfiles = (runfiles or _runfiles()).merge_all(
        ([data_runfiles] if data_runfiles else []) +
        ([default_runfiles] if default_runfiles else []),
    )
    return {
        "files": files or depset(),
        "files_to_run": FilesToRunProvider(
            # Copy fields instead of embedding the runfiles object into
            # FilesToRunProvider. This reduces the size of DefaultInfo
            # significantly.
            _runfiles_files = merged_runfiles.files,
            _runfiles_symlinks = merged_runfiles.symlinks,
            _runfiles_root_symlinks = merged_runfiles.root_symlinks,
            executable = executable,
            repo_mapping_manifest = None,
            runfiles_manifest = None,
        ),
    }

def _default_info_runfiles(r):
    # There is no point in storing the runfiles both in DefaultInfo and
    # the FilesToRunProvider contained within. Simply let
    # DefaultInfo.{data,default}_runfiles return the runfiles contained
    # in the FilesToRunProvider.
    return runfiles(
        files = r.files_to_run._runfiles_files,
        symlinks = r.files_to_run._runfiles_symlinks,
        root_symlinks = r.files_to_run._runfiles_root_symlinks,
    )

DefaultInfo, _DefaultInfoRaw = provider(
    computed_fields = {
        "data_runfiles": _default_info_runfiles,
        "default_runfiles": _default_info_runfiles,
    },
    init = _default_info_init,
)

def _java_info_init(
        output_jar,
        compile_jar,
        source_jar = None,
        compile_jdeps = None,
        generated_class_jar = None,
        generated_source_jar = None,
        native_headers_jar = None,
        manifest_proto = None,
        neverlink = False,
        deps = [],
        runtime_deps = [],
        exports = [],
        exported_plugins = [],
        jdeps = None,
        native_libraries = []):
    return {}

JavaInfo, _JavaInfoRaw = provider(
    init = _java_info_init,
)

def _run_environment_info_init(environment = {}, inherited_environment = []):
    return {
        "environment": environment,
        "inherited_environment": inherited_environment,
    }

RunEnvironmentInfo, _RunEnvironmentInfoRaw = provider(init = _run_environment_info_init)

def _template_variable_info_init(variables):
    return {"variables": variables}

TemplateVariableInfo, _TemplateVariableInfoRaw = provider(init = _template_variable_info_init)

def _cc_libc_top_alias_impl(ctx):
    fail("TODO")

cc_libc_top_alias = rule(
    implementation = _cc_libc_top_alias_impl,
    needs = [],
)

def cc_toolchain_suite(**kwargs):
    pass

def _get_effective_constraint_value(constraint_setting, value_label):
    if value_label == constraint_setting.default_constraint_value:
        # Constraint value is equal to its default value, meaning it
        # should not be encoded explicitly as part of the configuration.
        # Require that the constraint setting is absent.
        return None
    return value_label

def _config_setting_impl(ctx):
    return [ConfigSettingInfo(
        # Convert constraint values to a dictionary of constraint
        # settings to values. If the provided constraint value is the
        # default we set it to None, because it effectively means the
        # constraint setting should not be part of the configuration.
        constraints = {
            constraint_value[ConstraintValueInfo].constraint.label: _get_effective_constraint_value(
                constraint_value[ConstraintValueInfo].constraint,
                constraint_value[ConstraintValueInfo].label,
            )
            for constraint_value in ctx.attr.constraint_values
        },
        flag_values = {
            key.original_label: value
            for key, value in ctx.attr.flag_values.items()
        },
    )]

def _config_setting_init(**kwargs):
    # The "values" attr can be used to refer to command line options
    # that are integrated into Bazel. In our case we declare all of them
    # as build settings under @bazel_tools//command_line_option. This
    # allows us to simply remap "values" to "flag_values".
    kwargs["flag_values"] = kwargs.get("flag_values", {}) | {
        "@bazel_tools//command_line_option:" + option: value
        for option, value in kwargs.get("values", {}).items()
    }
    return kwargs

config_setting = rule(
    implementation = _config_setting_impl,
    attrs = {
        "constraint_values": attr.label_list(
            cfg = config.none(),
            providers = [ConstraintValueInfo],
        ),
        # TODO: Do we even want to support define_values?
        "define_values": attr.string_dict(),
        "flag_values": attr.label_keyed_string_dict(
            # We are only interested in obtaining the build setting
            # values, which doesn't require these targets to be
            # configured.
            cfg = config.unconfigured(),
        ),
        "values": attr.string_dict(),
    },
    initializer = _config_setting_init,
    needs = [],
    provides = [ConfigSettingInfo],
)

def configuration_field(fragment, name):
    # Don't provide actual support for late-bound defaults. Instead map
    # each of them to the respective command line option used by Bazel.
    if fragment == "apple":
        if name == "xcode_config_label":
            return Label("@bazel_tools//command_line_option:xcode_version_config")
    if fragment == "bazel_py":
        if name == "python_top":
            return Label("@bazel_tools//command_line_option:python_top")
    if fragment == "coverage":
        if name == "output_generator":
            # This configuration field should not map to lcov_merger if
            # coverage is disabled, as that would cause cyclic
            # dependencies otherwise. Let this map to an alias that only
            # points to lcov_merger if --collect_code_coverage is set.
            return Label("@bazel_tools//tools/coverage:coverage_output_generator")
    if fragment == "cpp":
        if name == "cs_fdo_profile":
            return Label("@bazel_tools//command_line_option:cs_fdo_profile")
        if name == "custom_malloc":
            return Label("@bazel_tools//command_line_option:custom_malloc")
        if name == "fdo_optimize":
            return Label("@bazel_tools//command_line_option:fdo_optimize")
        if name == "fdo_prefetch_hints":
            return Label("@bazel_tools//command_line_option:fdo_prefetch_hints")
        if name == "fdo_profile":
            return Label("@bazel_tools//command_line_option:fdo_profile")
        if name == "libc_top":
            return Label("@bazel_tools//command_line_option:grte_top")
        if name == "memprof_profile":
            return Label("@bazel_tools//command_line_option:memprof_profile")
        if name == "propeller_optimize":
            return Label("@bazel_tools//command_line_option:propeller_optimize")
        if name == "proto_profile_path":
            return Label("@bazel_tools//command_line_option:proto_profile_path")
        if name == "target_libc_top_DO_NOT_USE_ONLY_FOR_CC_TOOLCHAIN":
            return None
        if name == "xbinary_fdo":
            return Label("@bazel_tools//command_line_option:xbinary_fdo")
        if name == "zipper":
            return None
    if fragment == "java":
        if name == "launcher":
            return Label("@bazel_tools//command_line_option:java_launcher")
        if name == "java_toolchain_bytecode_optimizer":
            return Label("@bazel_tools//command_line_option:proguard_top")
        if name == "local_java_optimization_configuration":
            return Label("@bazel_tools//command_line_option:experimental_local_java_optimization_configuration")
    if fragment == "proto":
        if name == "proto_compiler":
            return Label("@bazel_tools//command_line_option:proto_compiler")
        if name == "proto_toolchain_for_cc":
            return Label("@bazel_tools//command_line_option:proto_toolchain_for_cc")
        if name == "proto_toolchain_for_java":
            return Label("@bazel_tools//command_line_option:proto_toolchain_for_java")
        if name == "proto_toolchain_for_java_lite":
            return Label("@bazel_tools//command_line_option:proto_toolchain_for_javalite")
    if fragment == "py":
        if name == "native_rules_allowlist":
            return Label("@bazel_tools//command_line_option:python_native_rules_allowlist")

    fail("this implementation of configuration_field() does not support fragment %s and name %s" % (fragment, name))

def _constraint_setting_impl(ctx):
    default_constraint_value = ctx.attr.default_constraint_value
    return [ConstraintSettingInfo(
        default_constraint_value = default_constraint_value.original_label if default_constraint_value else None,
        has_default_constraint_value = bool(default_constraint_value),
        label = ctx.label,
    )]

constraint_setting = rule(
    implementation = _constraint_setting_impl,
    attrs = {
        "default_constraint_value": attr.label(
            # Prevent cyclic dependency between the constraint_setting()
            # and the default constraint_value().
            cfg = config.unconfigured(),
            providers = [ConstraintValueInfo],
        ),
    },
    needs = [],
    provides = [ConstraintSettingInfo],
)

def _constraint_value_impl(ctx):
    constraint_setting = ctx.attr.constraint_setting[ConstraintSettingInfo]
    return [
        # Also provide a ConfigSettingInfo containing just this
        # constraint. This allows constraint values to be passed to
        # select() directly.
        ConfigSettingInfo(
            constraints = {
                constraint_setting.label: _get_effective_constraint_value(constraint_setting, ctx.label),
            },
            flag_values = {},
        ),
        ConstraintValueInfo(
            constraint = constraint_setting,
            label = ctx.label,
        ),
    ]

constraint_value = rule(
    implementation = _constraint_value_impl,
    attrs = {
        "constraint_setting": attr.label(
            mandatory = True,
            providers = [ConstraintSettingInfo],
        ),
    },
    needs = [],
    provides = [ConfigSettingInfo, ConstraintValueInfo],
)

def _filegroup_impl(ctx):
    files = []
    runfiles = []
    if ctx.attr.output_group:
        for src in ctx.attr.srcs:
            files.append(getattr(src[OutputGroupInfo], ctx.attr.output_group))
    elif len(ctx.attr.srcs) == 1:
        # If exactly one target is provided, return the original
        # DefaultInfo. This ensures that fields like files_to_run are
        # preserved.
        return ctx.attr.srcs[0][DefaultInfo]
    else:
        for src in ctx.attr.srcs:
            default_info = src[DefaultInfo]
            files.append(default_info.files)
            runfiles.append(default_info.default_runfiles)

    for data in ctx.attr.data:
        runfiles.append(data[DefaultInfo].default_runfiles)

    return [DefaultInfo(
        files = depset(direct = [], transitive = files),
        runfiles = ctx.runfiles(
            files = ctx.files.data,
        ).merge_all(runfiles),
    )]

filegroup = rule(
    implementation = _filegroup_impl,
    attrs = {
        "data": attr.label_list(allow_files = True),
        "output_group": attr.string(),
        "srcs": attr.label_list(allow_files = True),
    },
    needs = [],
)

def _genrule_impl(ctx):
    # Determine Make variables specific to genrule().
    outs = [out.path for out in ctx.outputs.outs]
    ruledir = "/".join([
        part
        for part in [ctx.bin_dir.path, ctx.label.workspace_root, ctx.label.package]
        if part
    ])
    srcs = [src.path for src in ctx.files.srcs]
    additional_substitutions = {
        "@D": ctx.outputs.outs[0].path.rsplit("/", 1)[0] if len(ctx.outputs.outs) == 1 else ruledir,
        "OUTS": " ".join(outs),
        "RULEDIR": ruledir,
        "SRCS": " ".join(srcs),
    }
    if len(outs) == 1:
        additional_substitutions["@"] = outs[0]
    if len(srcs) == 1:
        additional_substitutions["<"] = srcs[0]

    ctx.actions.run(
        executable = "/bin/bash",
        arguments = [
            "-c",
            ("source %s; " % ctx.file._genrule_setup.path) +
            ctx.expand_make_variables(
                "cmd",
                ctx.expand_location(
                    ctx.attr.cmd_bash or ctx.attr.cmd,
                    [
                        target
                        for attr in [ctx.attr.srcs, ctx.attr.tools]
                        for target in attr
                    ],
                ),
                additional_substitutions,
            ),
        ],
        inputs = [ctx.file._genrule_setup] + ctx.files.srcs,
        tools = [tool.files_to_run for tool in ctx.attr.tools],
        outputs = ctx.outputs.outs,
    )
    return [DefaultInfo(files = depset(ctx.outputs.outs))]

genrule = rule(
    implementation = _genrule_impl,
    attrs = {
        "cmd": attr.string(),
        "cmd_bash": attr.string(),
        "cmd_bat": attr.string(),
        "cmd_ps": attr.string(),
        "executable": attr.bool(),
        "local": attr.bool(),
        "message": attr.string(),
        "output_licenses": attr.string_list(),
        "output_to_bindir": attr.bool(),
        "outs": attr.output_list(mandatory = True),
        "srcs": attr.label_list(allow_files = True),
        "tools": attr.label_list(allow_files = True, cfg = "exec"),
        "_genrule_setup": attr.label(
            allow_single_file = True,
            default = "@bazel_tools//tools/genrule:genrule-setup.sh",
        ),
    },
    needs = [
        "default_exec_group",
        "make_variables",
    ],
)

def _java_plugins_flag_alias_impl(ctx):
    return [JavaPluginInfo()]

java_plugins_flag_alias = rule(
    implementation = _java_plugins_flag_alias_impl,
    needs = [],
)

def licenses(license_types):
    # This function is deprecated. Licenses can nowadays be attached in
    # the form of metadata. Provide a no-op stub.
    pass

def _platform_impl(ctx):
    # Convert all constraint values to a dict mapping the constraint
    # setting to the corresponding value.
    constraints = {}
    for value in ctx.attr.constraint_values:
        value_info = value[ConstraintValueInfo]
        setting_label = value_info.constraint.label
        value_label = value_info.label
        if setting_label in constraints:
            fail("constraint_values contains multiple values for constraint setting %s: %s and %s" % (
                setting_label,
                constraints[setting_label],
                value_label,
            ))
        constraints[setting_label] = _get_effective_constraint_value(value_info.constraint, value_label)

    exec_pkix_public_key = ctx.attr.exec_pkix_public_key
    repository_os_arch = ctx.attr.repository_os_arch
    repository_os_environ = ctx.attr.repository_os_environ
    repository_os_name = ctx.attr.repository_os_name

    # Inherit properties from the parent platform.
    if ctx.attr.parents:
        if len(ctx.attr.parents) != 1:
            fail("providing multiple parents is not supported")
        parent = ctx.attr.parents[0][PlatformInfo]
        constraints = parent.constraints | constraints
        exec_pkix_public_key = exec_pkix_public_key or parent.exec_pkix_public_key
        repository_os_arch = repository_os_arch or parent.repository_os_arch
        repository_os_environ = repository_os_environ or parent.repository_os_environ
        repository_os_name = repository_os_name or parent.repository_os_name

    return [PlatformInfo(
        constraints = {
            setting: value
            for setting, value in constraints.items()
            if value
        },
        exec_pkix_public_key = exec_pkix_public_key,
        repository_os_arch = repository_os_arch,
        repository_os_environ = repository_os_environ,
        repository_os_name = repository_os_name,
    )]

platform = rule(
    implementation = _platform_impl,
    attrs = {
        "constraint_values": attr.label_list(
            doc = """
            The combination of constraint choices that this platform
            comprises. In order for a platform to apply to a given
            environment, the environment must have at least the values
            in this list.

            Each constraint_value in this list must be for a different
            constraint_setting. For example, you cannot define a
            platform that requires the cpu architecture to be both
            @platforms//cpu:x86_64 and @platforms//cpu:arm.
            """,
            providers = [ConstraintValueInfo],
        ),
        "exec_pkix_public_key": attr.string(
            doc = """
            When the platform is used for execution, the elliptic-curve
            public key in PKIX form that identifies the execution
            platform. The key needs to be provided in base64 encoded
            form, without the PEM header/footer.
            """,
        ),
        "parents": attr.label_list(
            doc = """
            The label of a platform target that this platform should
            inherit from. Although the attribute takes a list, there
            should be no more than one platform present. Any
            constraint_settings not set directly on this platform will
            be found in the parent platform. See the section on Platform
            Inheritance for details.
            """,
            providers = [PlatformInfo],
        ),
        "repository_os_arch": attr.string(
            doc = """
            If this platform is used as a platform for executing
            commands as part of module extensions or repository rules,
            the name of the architecture to announce via
            repository_os.arch.

            This attribute should match the value of the "os.arch" Java
            property converted to lower case (e.g., "aarch64" for ARM64,
            "amd64" for x86-64, "x86" for x86-32).
            """,
        ),
        "repository_os_environ": attr.string_dict(
            doc = """
            If this platform is used as a platform for executing
            commands as part of module extensions or repository rules,
            environment variables to announce via repository_os.environ.
            """,
        ),
        "repository_os_name": attr.string(
            doc = """
            If this platform is used as a platform for executing
            commands as part of module extensions or repository rules,
            the operating system name to announce via
            repository_os.name.

            This attribute should match the value of the "os.name" Java
            property converted to lower case (e.g., "linux", "mac os x",
            "windows 10").
            """,
        ),
    },
    needs = [],
    provides = [PlatformInfo],
)

def _starlark_doc_extract_impl(ctx):
    fail("TODO: implement")

starlark_doc_extract = rule(
    _starlark_doc_extract_impl,
    attrs = {
        "deps": attr.label_list(),
        "src": attr.label(mandatory = True),
    },
    needs = [],
)

def _test_suite_impl(ctx):
    fail("TODO: implement")

test_suite = rule(
    _test_suite_impl,
    attrs = {
        "tests": attr.string_list(),
    },
    needs = [],
)

def _toolchain_impl(ctx):
    return [DeclaredToolchainInfo(
        target_settings = [
            target_setting.label
            for target_setting in ctx.attr.target_settings
        ],
        toolchain = ctx.attr.toolchain.original_label,
        toolchain_type = ctx.attr.toolchain_type[ToolchainTypeInfo].type_label,
        use_target_platform_constraints = ctx.attr.use_target_platform_constraints,
    )]

toolchain = rule(
    implementation = _toolchain_impl,
    attrs = {
        "target_settings": attr.label_list(
            providers = [ConfigSettingInfo],
        ),
        "toolchain": attr.label(
            # Prevent configuring toolchains that are not used.
            cfg = config.unconfigured(),
            mandatory = True,
            providers = [ToolchainInfo],
        ),
        "toolchain_type": attr.label(
            mandatory = True,
            providers = [ToolchainTypeInfo],
        ),
        "use_target_platform_constraints": attr.bool(),
    },
    needs = [],
    provides = [DeclaredToolchainInfo],
)

def _toolchain_type_impl(ctx):
    return [ToolchainTypeInfo(
        type_label = ctx.label,
    )]

toolchain_type = rule(
    implementation = _toolchain_type_impl,
    needs = [],
    provides = [ToolchainTypeInfo],
)

def coverage_common_instrumented_files_info(
        ctx,
        *,
        coverage_environment = {},
        coverage_support_files = [],
        dependency_attributes = [],
        extensions = None,
        metadata_files = [],
        reported_to_actual_sources = None,
        source_attributes = []):
    return InstrumentedFilesInfo(
        # TODO: instrumented_files.
        metadata_files = depset(metadata_files),
    )

def proto_common_do_not_use_external_proto_infos():
    return []

def proto_common_do_not_use_incompatible_enable_proto_toolchain_resolution():
    # This option be controlled by command line option
    # --incompatible_enable_proto_toolchain_resolution.
    return False

def builtins_internal_apple_common_dotted_version(v):
    # TODO: Provide a proper implementation.
    return v

def builtins_internal_cc_common_action_is_enabled(*, feature_configuration, action_name):
    return action_name in feature_configuration._enabled_action_config_action_names

def _selectable_get_name(selectable):
    if selectable.type_name == "action_config":
        return selectable.action_name
    return selectable.name

def feature_configuration_is_enabled(feature_configuration):
    enabled_feature_names = feature_configuration._enabled_feature_names
    return lambda feature: feature in enabled_feature_names

FeatureConfiguration = provider(
    computed_fields = {
        "is_enabled": feature_configuration_is_enabled,
    },
)

def _get_feature_configuration(
        requested_features,
        selectables_by_name,
        selectables,
        provides,
        implies,
        implied_by,
        requires,
        required_by,
        action_configs_by_action_name,
        cc_toolchain_path):
    requested_selectables = set([
        name
        for name in requested_features
        if name in selectables_by_name
    ])

    enabled = set()

    def enable_all_implied_by(selectable):
        # Bazel's implementation uses recursion, which Starlark does not
        # permit. Add some dummy loops to work around this.
        queue = set([selectable])
        for dummy1 in selectables_by_name:
            for dummy2 in selectables_by_name:
                if not queue:
                    return
                selectable = queue.pop()
                if selectable not in enabled:
                    enabled.add(selectable)
                    for implied in implies.get(selectable, set()):
                        queue.add(implied)
        fail("enable_all_implied_by failed to process all selectables")

    def is_implied_by_enabled_activatable(selectable):
        return not implied_by[selectable].isdisjoint(enabled)

    def all_implications_enabled(selectable):
        for implied in implies.get(selectable, set()):
            if implied not in enabled:
                return False
        return True

    def all_requirements_met(feature):
        if feature not in requires:
            return True
        for requires_all_of in requires[feature]:
            requirement_met = True
            for required in requires_all_of:
                if not required in enabled:
                    requirement_met = False
            if requirement_met:
                return True
        return False

    def is_satisfied(selectable):
        return (
            (selectable in requested_selectables or is_implied_by_enabled_activatable(selectable)) and
            all_implications_enabled(selectable) and
            all_requirements_met(selectable)
        )

    def check_activatable(selectable):
        if selectable not in enabled or is_satisfied(selectable):
            return
        enabled.remove(selectable)

        for implies_current in implied_by.get(selectable, []):
            check_activatable(implies_current)
        for requires_current in required_by.get(selectable, []):
            check_activatable(requires_current)
        for implied in implies.get(selectable, []):
            check_activatable(implied)

    def disable_unsupported_activatables():
        check = set(enabled)
        for i in check:
            check_activatable(i)

    def is_feature(activatable):
        return {
            "action_config": False,
            "feature": True,
        }[activatable.type_name]

    def is_action_config(activatable):
        return {
            "action_config": True,
            "feature": False,
        }[activatable.type_name]

    # From FeatureSelection.run():
    for selectable in requested_selectables:
        enable_all_implied_by(selectable)
    disable_unsupported_activatables()
    enabled_activatables_in_order_builder = []
    for selectable in selectables:
        if _selectable_get_name(selectable) in enabled:
            enabled_activatables_in_order_builder.append(selectable)

    enabled_activatables_in_order = enabled_activatables_in_order_builder
    enabled_features_in_order = [
        activatable
        for activatable in enabled_activatables_in_order
        if is_feature(activatable)
    ]
    enabled_action_configs_in_order = [
        activatable
        for activatable in enabled_activatables_in_order
        if is_action_config(activatable)
    ]

    for provided in provides:
        conflicts = []
        for selectable_providing_string in provides[provided]:
            if selectable_providing_string in enabled_activatables_in_order:
                conflicts.append(selectable_providing_string.name)

        if len(conflicts) > 1:
            fail("Symbol %s is provided by all of the following features: %s" % (provided, " ".join(conflicts)))

    enabled_action_config_names = set([
        action_config.action_name
        for action_config in enabled_action_configs_in_order
    ])

    enabled_feature_names = set([
        feature.name
        for feature in enabled_features_in_order
    ])
    return FeatureConfiguration(
        _action_config_by_action_name = action_configs_by_action_name,
        _cc_toolchain_path = cc_toolchain_path,
        _enabled_action_config_action_names = enabled_action_config_names,
        _enabled_features = enabled_features_in_order,
        _enabled_feature_names = enabled_feature_names,
        is_requested = lambda feature: feature in requested_features,
    )

def env_entry_can_be_expanded(env_entry, variables):
    if (
        env_entry.expand_if_available and
        not variables_is_available(variables, env_entry.expand_if_available)
    ):
        return False
    return True

def env_entry_add_env_entry(env_entry, variables, env_builder):
    if not env_entry_can_be_expanded(env_entry, variables):
        return
    env_builder[env_entry.key] = env_entry.value

def env_set_expand_environment(env_set, action, variables, enabled_feature_names, env_builder):
    if action not in env_set.actions:
        return
    if not is_with_features_satisfied(env_set.with_features, enabled_feature_names):
        return
    for env_entry in env_set.env_entries:
        env_entry_add_env_entry(env_entry, variables, env_builder)

def feature_expand_environment(feature, action, variables, enabled_feature_names, env_builder):
    for env_set in feature.env_sets:
        env_set_expand_environment(
            env_set,
            action,
            variables,
            enabled_feature_names,
            env_builder,
        )

def builtins_internal_cc_common_get_environment_variables(
        feature_configuration,
        action_name,
        variables):
    env_builder = {}
    for feature in feature_configuration._enabled_features:
        feature_expand_environment(
            feature,
            action_name,
            variables,
            feature_configuration._enabled_feature_names,
            env_builder,
        )
    return env_builder

def builtins_internal_cc_common_get_execution_requirements(
        *,
        action_name,
        feature_configuration):
    return []

def flag_expand(flag, variables, command_line):
    expanded = ""
    mode = 0
    variable_name = ""
    for c in flag.elems():
        if mode == 0:
            if c == "%":
                mode = 1
            else:
                expanded += c
        elif mode == 1:
            if c == "%":
                expanded += c
            elif c == "{":
                mode = 2
            else:
                fail("% not followed by % or {")
        elif mode == 2:
            if c == "}":
                v = variables_get_variable(variables, variable_name)
                if type(v) == "File":
                    v = v.path
                expanded += v
                variable_name = ""
                mode = 0
            else:
                variable_name += c

    command_line.append(expanded)

def variables_is_available(variables, variable):
    if variable in variables:
        return True
    for part in variable.split("."):
        if type(variables) == "dict":
            if part not in variables:
                return False
            variables = variables[part]
        elif type(variables) == "struct":
            if not hasattr(variables, part):
                return False
            variables = getattr(variables, part)
        else:
            fail("unknown type in value of part %s of variable %s: %s" % (part, variable, type(variables)))
    return True

def variables_get_variable(variables, variable):
    if variable in variables:
        return variables[variable]
    for part in variable.split("."):
        if type(variables) == "dict":
            variables = variables[part]
        elif type(variables) == "struct":
            variables = getattr(variables, part)
        else:
            fail("unknown type in value of part %s of variable %s: %s" % (part, variable, type(variables)))
    return variables

def flag_group_can_be_expanded(flag_group, variables):
    if (
        flag_group.expand_if_available and
        not variables_is_available(variables, flag_group.expand_if_available)
    ):
        return False
    if (
        flag_group.expand_if_not_available and
        variables_is_available(variables, flag_group.expand_if_not_available)
    ):
        return False
    if flag_group.expand_if_true and (
        not variables_is_available(variables, flag_group.expand_if_true) or
        not bool(variables_get_variable(variables, flag_group.expand_if_true))
    ):
        return False
    if flag_group.expand_if_false and (
        not variables_is_available(variables, flag_group.expand_if_false) or
        bool(variables_get_variable(variables, flag_group.expand_if_false))
    ):
        return False
    if flag_group.expand_if_equal and (
        not variables_is_available(variables, flag_group.expand_if_equal.name) or
        variables_get_variable(variables, flag_group.expand_if_equal.name) != flag_group.expand_if_equal.value
    ):
        return False
    return True

def flag_group_expand_command_line(flag_group, variables, command_line):
    if not flag_group_can_be_expanded(flag_group, variables):
        return
    if flag_group.iterate_over:
        variable_values = variables_get_variable(variables, flag_group.iterate_over)
        if type(variable_values) == "depset":
            variable_values = variable_values.to_list()
        for variable_value in variable_values:
            nested_variables = dict(variables)
            nested_variables[flag_group.iterate_over] = variable_value
            for expandable in flag_group.flags:
                flag_expand(expandable, nested_variables, command_line)
            for expandable in flag_group.flag_groups:
                flag_group_expand_command_line(expandable, nested_variables, command_line)
    else:
        for expandable in flag_group.flags:
            flag_expand(expandable, variables, command_line)
        for expandable in flag_group.flag_groups:
            flag_group_expand_command_line(expandable, variables, command_line)

def is_with_features_satisfied(with_feature_sets, enabled_feature_names):
    if not with_feature_sets:
        return True
    for feature_set in with_feature_sets:
        if (
            enabled_feature_names.issuperset(feature_set.features) and
            enabled_feature_names.isdisjoint(feature_set.not_features)
        ):
            return True
    return False

def flag_set_expand_command_line(flag_set, action, variables, enabled_feature_names, command_line):
    # TODO: Do we need to do anything with expand_if_all_available?
    if not is_with_features_satisfied(flag_set.with_features, enabled_feature_names):
        return
    if action not in flag_set.actions:
        return
    for flag_group in flag_set.flag_groups:
        flag_group_expand_command_line(flag_group, variables, command_line)

def action_config_expand_command_line(action_config, variables, enabled_feature_names, command_line):
    for flag_set in action_config.flag_sets:
        flag_set_expand_command_line(
            flag_set,
            action_config.action_name,
            variables,
            enabled_feature_names,
            command_line,
        )

def feature_expand_command_line(feature, action, variables, enabled_feature_names, command_line):
    for flag_set in feature.flag_sets:
        flag_set_expand_command_line(
            flag_set,
            action,
            variables,
            enabled_feature_names,
            command_line,
        )

def builtins_internal_cc_common_get_memory_inefficient_command_line(
        feature_configuration,
        action_name,
        variables):
    command_line = []
    if action_name in feature_configuration._enabled_action_config_action_names:
        action_config_expand_command_line(
            feature_configuration._action_config_by_action_name[action_name],
            variables,
            feature_configuration._enabled_feature_names,
            command_line,
        )
    for feature in feature_configuration._enabled_features:
        feature_expand_command_line(
            feature,
            action_name,
            variables,
            feature_configuration._enabled_feature_names,
            command_line,
        )
    return command_line

def _tool_get_tool_path_string(tool, cc_toolchain_path):
    p = tool.path
    if cc_toolchain_path and not p.startswith("/"):
        p = cc_toolchain_path + "/" + p
    return p

def _tool_is_with_features_satisfied(with_feature_sets, enabled_feature_names):
    if not with_feature_sets:
        return True
    for feature_set in with_feature_sets:
        fail("TODO: match feature_set.features and feature_set.not_features!")
    return False

def _action_config_get_tool(action_config, enabled_feature_names):
    for tool in action_config.tools:
        if _tool_is_with_features_satisfied(tool.with_features, enabled_feature_names):
            return tool
    fail("Matching tool for action %s not found for given feature configuration" % action_config.action_name)

def builtins_internal_cc_common_get_tool_for_action(feature_configuration, action_name):
    action_config = feature_configuration._action_config_by_action_name[action_name]
    return _tool_get_tool_path_string(
        _action_config_get_tool(action_config, feature_configuration._enabled_feature_names),
        feature_configuration._cc_toolchain_path,
    )

def builtins_internal_cc_common_get_tool_requirement_for_action(*, action_name, feature_configuration):
    return []

def builtins_internal_cc_internal_actions2ctx_cheat(actions):
    return native.current_ctx()

def builtins_internal_cc_internal_cc_toolchain_features(*, toolchain_config_info, tools_directory):
    selectables = []
    selectables_by_name = {}
    action_configs_by_action_name = {}
    default_selectables = []
    for feature in toolchain_config_info._features_DO_NOT_USE:
        selectables.append(feature)
        selectables_by_name[feature.name] = feature
        if feature.enabled:
            default_selectables.append(feature.name)

    for action_config in toolchain_config_info._action_configs_DO_NOT_USE:
        selectables.append(action_config)
        selectables_by_name[action_config.action_name] = action_config
        action_configs_by_action_name[action_config.action_name] = action_config
        if action_config.enabled:
            default_selectables.append(action_config.action_name)

    implies = {}
    requires = {}
    provides = {}
    implied_by = {}
    required_by = {}

    for feature in toolchain_config_info._features_DO_NOT_USE:
        name = feature.name
        for required_features in feature.requires:
            all_of = set()
            for required_name in required_features.features:
                all_of.add(required_name)
                required_by.setdefault(required_name, set()).add(name)
            requires.setdefault(name, set()).union(all_of)
        for implied_name in feature.implies:
            implied_by.setdefault(implied_name, set()).add(name)
            implies.setdefault(name, set()).add(implied_name)
        for provides_name in feature.provides:
            provides.setdefault(provides_name, set()).add(name)

    for action_config in toolchain_config_info._action_configs_DO_NOT_USE:
        name = action_config.action_name
        for implied_name in action_config.implies:
            implied_by.setdefault(implied_name, set()).add(name)
            implies.setdefault(name, set()).add(implied_name)

    def configure_features(requested_features):
        return _get_feature_configuration(
            requested_features,
            selectables_by_name,
            selectables,
            provides,
            implies,
            implied_by,
            requires,
            required_by,
            action_configs_by_action_name,
            tools_directory,
        )

    return struct(
        _artifact_name_patterns = {
            pattern.category_name: pattern
            for pattern in toolchain_config_info._artifact_name_patterns_DO_NOT_USE
        },
        configure_features = configure_features,
        default_features_and_action_configs = lambda: default_selectables,
    )

def builtins_internal_cc_internal_cc_toolchain_variables(vars):
    return vars

def builtins_internal_cc_internal_check_private_api(*, allowlist, depth = 1):
    pass

def builtins_internal_cc_internal_collect_libraries_to_link(
        libraries_to_link,
        cc_toolchain,
        feature_configuration,
        output,
        dynamic_library_solib_symlink_output,
        link_type,
        linking_mode,
        is_native_deps,
        solib_dir,
        toolchain_libraries_solib_dir,
        workspace_name):
    return struct(
        all_runtime_library_search_directories = depset(),
        library_search_directories = depset(),
    )

def builtins_internal_cc_internal_combine_cc_toolchain_variables(parent, *variables):
    for v in variables:
        parent = parent | v
    return parent

def builtins_internal_cc_internal_compute_output_name_prefix_dir(*, configuration, purpose):
    return ""

def builtins_internal_cc_internal_convert_library_to_link_list_to_linker_input_list(libraries_to_link, static_mode, for_dynamic_library, support_dynamic_linker):
    library_inputs = []
    for library_to_link in libraries_to_link.to_list():
        fail("TODO: implement!")
    return library_inputs

def builtins_internal_cc_internal_create_cc_compile_action(
        *,
        action_construction_context,
        cc_compilation_context,  # TODO
        cc_toolchain,  # TODO
        compile_build_variables,
        configuration,  # TODO
        feature_configuration,
        source,  # TODO
        toolchain_type,  # TODO
        action_name = None,
        additional_compilation_inputs = [],
        additional_compilation_inputs_set = None,
        additional_include_scanning_roots = [],  # TODO
        additional_outputs = [],  # TODO
        additional_prunable_headers = None,  # TODO
        build_info_header_files = None,  # TODO
        cache_key_inputs = None,  # TODO
        copts_filter = None,  # TODO
        diagnostics_file = None,  # TODO
        dotd_file = None,  # TODO
        dwo_file = None,  # TODO
        gcno_file = None,  # TODO
        lto_indexing_file = None,  # TODO
        modmap_file = None,  # TODO
        modmap_input_file = None,  # TODO
        module_files = None,  # TODO
        needs_include_validation = False,  # TODO
        output_file = None,
        progress_message_prefix = None,  # TODO
        shareable = None,  # TODO
        should_scan_includes = None,  # TODO
        use_pic = False):
    # From CppCompileActionBuilder.getActionName():
    if not action_name:
        source_path = source.path
        if source_path.endswith(".cppmap"):
            action_name = "c++-module-compile"
        elif source_path.endswith((".h", ".hh", ".hpp", ".ipp", ".hxx", ".h++", ".inc", ".inl", ".tlh", ".tli", ".H", ".tcc")):
            if not feature_configuration.is_enabled("parse_headers"):
                fail("header files can only be used as source files if header parsing is enabled")
            action_name = "c++-header-parsing"
        elif source_path.endswith(".c"):
            action_name = "c-compile"
        elif source_path.endswith((".cc", ".cpp", ".cxx", ".c++", ".C", ".cu", ".cl")):
            action_name = "c++-compile"
        elif source_path.endswith(".m"):
            action_name = "objc-compile"
        elif source_path.endswith(".mm"):
            action_name = "objc++-compile"
        elif source_path.endswith(".s"):
            action_name = "assemble"
        elif source_path.endswith(".S"):
            action_name = "preprocess-assemble"
        elif source_path.endswith(".ipb"):
            action_name = "clif-match"
        elif source_path.endswith((".pcm", ".gcm", ".ifc")):
            action_name = "c++-module-codegen"
        else:
            fail("cannot infer action name for source file " + source.path)

    direct_inputs = [source]
    if additional_compilation_inputs:
        direct_inputs += additional_compilation_inputs
    transitive_inputs = [cc_toolchain.all_files, cc_compilation_context.headers]
    if additional_compilation_inputs_set:
        transitive_inputs.append(additional_compilation_inputs_set)
    inputs = depset(
        direct = direct_inputs,
        transitive = transitive_inputs,
    )

    outputs = []
    if output_file:
        outputs.append(output_file)
    if dotd_file:
        outputs.append(dotd_file)

    action_construction_context.actions.run(
        executable = builtins_internal_cc_common_get_tool_for_action(feature_configuration, action_name),
        arguments = builtins_internal_cc_common_get_memory_inefficient_command_line(feature_configuration, action_name, compile_build_variables),
        env = builtins_internal_cc_common_get_environment_variables(feature_configuration, action_name, compile_build_variables),
        inputs = inputs,
        outputs = outputs,
    )

def builtins_internal_cc_internal_create_header_info(
        *,
        header_module = None,
        modular_private_headers = [],
        modular_public_headers = [],
        pic_header_module = None,
        separate_module = None,
        separate_module_headers = [],
        separate_pic_module = None,
        textual_headers = []):
    return struct(
        header_module = header_module,
        modular_private_headers = modular_private_headers,
        modular_public_headers = modular_public_headers,
        pic_header_module = pic_header_module,
        separate_module = separate_module,
        separate_module_headers = separate_module_headers,
        separate_pic_module = separate_pic_module,
        textual_headers = textual_headers,
    )

def builtins_internal_cc_internal_create_header_info_with_deps(
        *,
        header_info = None,
        deps = [],
        merged_deps = []):
    return struct(
        header_module = header_info.header_module,
        modular_private_headers = header_info.modular_private_headers,
        modular_public_headers = header_info.modular_public_headers,
        pic_header_module = header_info.pic_header_module,
        separate_module = header_info.separate_module,
        separate_module_headers = header_info.separate_module_headers,
        separate_pic_module = header_info.separate_pic_module,
        textual_headers = header_info.textual_headers,
    )

def builtins_internal_cc_internal_create_cpp_source(*, label, source, type):
    return struct(__todo_is_cpp_source = True)

def builtins_internal_cc_internal_create_copts_filter(copts_filter = None):
    if copts_filter:
        fail("TODO: Support filtering")
    return struct(__todo_is_copts_filter = True)

def library_to_link_disable_whole_archive(lib):
    disable_whole_archive = lib._disable_whole_archive
    return lambda: lib._disable_whole_archive

def library_to_link_must_keep_debug(lib):
    must_keep_debug = lib._must_keep_debug
    return lambda: must_keep_debug

def library_to_link_objects_private(lib):
    object_files = lib._object_files
    return lambda: object_files.to_list()

def library_to_link_pic_objects_private(lib):
    pic_object_files = lib._pic_object_files
    return lambda: pic_object_files.to_list()

LibraryToLink = provider(
    computed_fields = {
        "disable_whole_archive": library_to_link_disable_whole_archive,
        "must_keep_debug": library_to_link_must_keep_debug,
        "objects_private": library_to_link_objects_private,
        "pic_objects_private": library_to_link_pic_objects_private,
    },
)

def builtins_internal_cc_internal_create_library_to_link(library_to_link):
    return LibraryToLink(
        _disable_whole_archive = getattr(library_to_link, "disable_whole_archive", False),
        _must_keep_debug = getattr(library_to_link, "must_keep_debug", False),
        _object_files = depset(getattr(library_to_link, "object_files", [])),
        _pic_object_files = depset(getattr(library_to_link, "pic_object_files", [])),
        alwayslink = getattr(library_to_link, "alwayslink", False),
        dynamic_library = getattr(library_to_link, "dynamic_library", None),
        interface_library = getattr(library_to_link, "interface_library", None),
        pic_static_library = getattr(library_to_link, "pic_static_library", None),
        resolved_symlink_dynamic_library = getattr(library_to_link, "resolved_symlink_dynamic_library", None),
        # Notice "resolve_" instead of "resolved_".
        resolved_symlink_interface_library = getattr(library_to_link, "resolve_symlink_interface_library", None),
        static_library = getattr(library_to_link, "static_library", None),
    )

def builtins_internal_cc_internal_create_module_map_action(
        *,
        actions,
        additional_exported_headers,
        compiled_module,
        dependent_module_maps,
        feature_configuration,
        generate_submodules,
        module_map_home_is_cwd,
        module_map,
        private_headers,
        public_headers,
        separate_module_headers,
        without_extern_dependencies):
    actions.run(
        executable = "false",
        outputs = [module_map.file()],
    )

def builtins_internal_cc_internal_create_shared_non_lto_artifacts(
        actions,
        lto_compilation_context,
        is_linker,
        feature_configuration,
        cc_toolchain,
        use_pic,
        object_files):
    shared_non_lto_backends = {}
    lto_bitcode_inputs = lto_compilation_context.lto_bitcode_inputs()
    for input_artifact in object_files:
        if input_artifact in lto_bitcode_inputs:
            fail("TODO")
    return shared_non_lto_backends

def builtins_internal_cc_internal_declare_compile_output_file(*, configuration, ctx, label, output_name):
    return ctx.actions.declare_file(output_name)

def _escaped_path(p):
    return "".join([
        "_U" if c == "_" else "_S" if c == "/" else "_B" if c == "\\" else "_C" if c == ":" else "_A" if c == "@" else c
        for c in p.elems()
    ])

def builtins_internal_cc_internal_dynamic_library_soname(actions, path, preserve_name):
    if preserve_name:
        return path.rsplit("/", 1)[-1]

    # TODO: This should include the name of the configuration.
    return "lib" + _escaped_path(path)

def builtins_internal_cc_internal_dynamic_library_symlink(actions, library, solib_directory, preserve_name, prefix_consumer):
    # TODO: The symlink is likely not created in the right location.
    # TODO: Respect prefix_consumer.
    symlink = actions.declare_file("%s/%s" % (
        solib_directory,
        builtins_internal_cc_internal_dynamic_library_soname(actions, library.path, preserve_name),
    ))
    actions.symlink(symlink, target_file = library)
    return symlink

def builtins_internal_cc_internal_exec_os(ctx):
    return "unknown"

def builtins_internal_cc_internal_escape_label(label):
    return _escaped_path(label.repo_name + "@" + label.package + ":" + label.name)

def builtins_internal_cc_internal_for_object_file(name, is_whole_archive):
    return struct(
        is_whole_archive = is_whole_archive,
        name = name,
        type = "object_file",
    )

def builtins_internal_cc_internal_for_static_library(name, is_whole_archive):
    return struct(
        is_whole_archive = is_whole_archive,
        name = name,
        type = "static_library",
    )

def builtins_internal_cc_internal_freeze(value):
    if type(value) == "list":
        return tuple(value)
    if type(value) == "dict":
        return tuple(value.items())
    return value

# Artifact name patterns that are registered by default.
# Obtained from ArtifactCategory.java.
default_artifact_name_patterns = {
    "STATIC_LIBRARY": struct(prefix = "lib", extension = ".a"),
    "ALWAYSLINK_STATIC_LIBRARY": struct(prefix = "lib", extension = ".lo"),
    "DYNAMIC_LIBRARY": struct(prefix = "lib", extension = ".so"),
    "EXECUTABLE": struct(prefix = "", extension = ""),
    "INTERFACE_LIBRARY": struct(prefix = "lib", extension = ".ifso"),
    "PIC_FILE": struct(prefix = "", extension = ".pic"),
    "INCLUDED_FILE_LIST": struct(prefix = "", extension = ".d"),
    "SERIALIZED_DIAGNOSTICS_FILE": struct(prefix = "", extension = ".dia"),
    "OBJECT_FILE": struct(prefix = "", extension = ".o"),
    "PIC_OBJECT_FILE": struct(prefix = "", extension = ".pic.o"),
    "CPP_MODULE": struct(prefix = "", extension = ".pcm"),
    "CPP_MODULE_GCM": struct(prefix = "", extension = ".gcm"),
    "CPP_MODULE_IFC": struct(prefix = "", extension = ".ifc"),
    "CPP_MODULES_INFO": struct(prefix = "", extension = ".CXXModules.json"),
    "CPP_MODULES_DDI": struct(prefix = "", extension = ".ddi"),
    "CPP_MODULES_MODMAP": struct(prefix = "", extension = ".modmap"),
    "CPP_MODULES_MODMAP_INPUT": struct(prefix = "", extension = ".modmap.input"),
    "GENERATED_ASSEMBLY": struct(prefix = "", extension = ".s"),
    "PROCESSED_HEADER": struct(prefix = "", extension = ".processed"),
    "GENERATED_HEADER": struct(prefix = "", extension = ".h"),
    "PREPROCESSED_C_SOURCE": struct(prefix = "", extension = ".i"),
    "PREPROCESSED_CPP_SOURCE": struct(prefix = "", extension = ".ii"),
    "COVERAGE_DATA_FILE": struct(prefix = "", extension = ".gcno"),
    "CLIF_OUTPUT_PROTO": struct(prefix = "", extension = ".opb"),
}

def builtins_internal_cc_internal_get_artifact_name_for_category(cc_toolchain, category, output_name):
    pattern = cc_toolchain._toolchain_features._artifact_name_patterns.get(category)
    if not pattern:
        pattern = default_artifact_name_patterns[category]

    output_parts = output_name.split("/")
    output_parts[-1] = pattern.prefix + output_parts[-1] + pattern.extension
    return "/".join(output_parts)

def builtins_internal_cc_internal_get_link_args(
        *,
        action_name,
        build_variables,
        feature_configuration,
        parameter_file_type):
    command_line = builtins_internal_cc_common_get_memory_inefficient_command_line(
        feature_configuration,
        action_name,
        build_variables,
    )

    # FromLinkCommandLine.getParamCommandLine():
    if variables_is_available(build_variables, "linker_param_file"):
        linker_param_file_value = variables_get_variable(build_variables, "linker_param_file")
        command_line = [
            v
            for v in command_line
            if linker_param_file_value not in v
        ]

    args = native.current_ctx().actions.args()
    args.add_all(command_line)
    return args

def builtins_internal_cc_internal_intern_seq(seq):
    return tuple(seq)

def builtins_internal_cc_internal_intern_string_sequence_variable_value(string_sequence):
    return string_sequence

def builtins_internal_cc_internal_is_tree_artifact(artifact):
    return artifact.is_directory

def builtins_internal_cc_internal_licenses(ctx):
    return None

def builtins_internal_cc_internal_per_file_copts(cpp_configuration, source_file, label):
    # TODO: Implement.
    return ()

def builtins_internal_cc_internal_wrap_link_actions(actions, build_config = None, use_shareable_artifact_factory = False):
    return actions

def builtins_internal_java_common_internal_do_not_use__check_java_toolchain_is_declared_on_rule():
    return "TODO"

def builtins_internal_java_common_internal_do_not_use__google_legacy_api_enabled():
    return "TODO"

def builtins_internal_java_common_internal_do_not_use__incompatible_java_info_merge_runtime_module_flags():
    return "TODO"

def builtins_internal_java_common_internal_do_not_use_check_provider_instances(providers, what, provider_type):
    # TODO.
    pass

def builtins_internal_java_common_internal_do_not_use_collect_native_deps_dirs():
    return "TODO"

def builtins_internal_java_common_internal_do_not_use_create_compilation_action():
    return "TODO"

def builtins_internal_java_common_internal_do_not_use_create_header_compilation_action():
    return "TODO"

def builtins_internal_java_common_internal_do_not_use_expand_java_opts():
    return "TODO"

def builtins_internal_java_common_internal_do_not_use_get_runtime_classpath_for_archive():
    return "TODO"

def builtins_internal_java_common_internal_do_not_use_incompatible_disable_non_executable_java_binary():
    return False

def builtins_internal_java_common_internal_do_not_use_target_kind():
    return "TODO"

def builtins_internal_java_common_internal_do_not_use_tokenize_javacopts():
    return "TODO"

def builtins_internal_java_common_internal_do_not_use_wrap_java_info():
    return "TODO"

def builtins_internal_py_builtins_are_action_listeners_enabled(ctx):
    return False

def builtins_internal_py_builtins_create_repo_mapping_manifest(ctx, runfiles, output):
    ctx.actions.write(output, "TODO")

def builtins_internal_py_builtins_get_current_os_name():
    return "unknown"

def builtins_internal_py_builtins_get_label_repo_runfiles_path(label):
    return "/".join(["..", label.repo_name] + label.package.split("/"))

def builtins_internal_py_builtins_get_legacy_external_runfiles(ctx):
    return False

def builtins_internal_py_builtins_is_bzlmod_enabled(ctx):
    return True

def builtins_internal_py_builtins_make_runfiles_respect_legacy_external_runfiles(ctx, runfiles):
    return runfiles

def builtins_internal_py_builtins_merge_runfiles_with_generated_inits_empty_files_supplier(ctx, runfiles):
    return runfiles

def builtins_json_encode_indent(x, **kwargs):
    return json.indent(json.encode(x), **kwargs)

exported_rules = {
    "alias": native.alias,
    "cc_libc_top_alias": cc_libc_top_alias,
    "cc_toolchain_suite": cc_toolchain_suite,
    "config_setting": config_setting,
    "constraint_setting": constraint_setting,
    "constraint_value": constraint_value,
    "exports_files": native.exports_files,
    "filegroup": filegroup,
    "genrule": genrule,
    "glob": native.glob,
    "java_plugins_flag_alias": java_plugins_flag_alias,
    "label_flag": native.label_flag,
    "label_setting": native.label_setting,
    "licenses": licenses,
    "package_group": native.package_group,
    "platform": platform,
    "starlark_doc_extract": starlark_doc_extract,
    "test_suite": test_suite,
    "toolchain": toolchain,
    "toolchain_type": toolchain_type,
}
exported_toplevels = {
    "AnalysisFailureInfo": AnalysisFailureInfo,
    "AnalysisTestResultInfo": AnalysisTestResultInfo,
    "DefaultInfo": DefaultInfo,
    "OutputGroupInfo": OutputGroupInfo,
    "RunEnvironmentInfo": RunEnvironmentInfo,
    "InstrumentedFilesInfo": InstrumentedFilesInfo,
    "JavaInfo": JavaInfo,
    "JavaPluginInfo": JavaPluginInfo,
    "PackageSpecificationInfo": PackageSpecificationInfo,
    "config_common": struct(
        FeatureFlagInfo = FeatureFlagInfo,
        toolchain_type = config_common.toolchain_type,
    ),
    "configuration_field": configuration_field,
    "coverage_common": struct(
        instrumented_files_info = coverage_common_instrumented_files_info,
    ),
    "exec_transition": transition,
    "json": struct(
        decode = json.decode,
        encode = json.encode,
        # starlark-go does not support json.encode_indent().
        # TODO: Should we get it added?
        encode_indent = builtins_json_encode_indent,
        indent = json.indent,
    ),
    "platform_common": struct(
        ConstraintValueInfo = ConstraintValueInfo,
        PlatformInfo = PlatformInfo,
        TemplateVariableInfo = TemplateVariableInfo,
        ToolchainInfo = ToolchainInfo,
    ),
    "proto_common_do_not_use": struct(
        external_proto_infos = proto_common_do_not_use_external_proto_infos,
        incompatible_enable_proto_toolchain_resolution = proto_common_do_not_use_incompatible_enable_proto_toolchain_resolution,
    ),
    "testing": struct(
        ExecutionInfo = ExecutionInfo,
    ),
}

exported_toplevels["_builtins"] = struct(
    internal = struct(
        apple_common = struct(
            dotted_version = builtins_internal_apple_common_dotted_version,
        ),
        cc_common = struct(
            action_is_enabled = builtins_internal_cc_common_action_is_enabled,
            do_not_use_tools_cpp_compiler_present = None,
            get_environment_variables = builtins_internal_cc_common_get_environment_variables,
            get_execution_requirements = builtins_internal_cc_common_get_execution_requirements,
            get_memory_inefficient_command_line = builtins_internal_cc_common_get_memory_inefficient_command_line,
            get_tool_for_action = builtins_internal_cc_common_get_tool_for_action,
            get_tool_requirement_for_action = builtins_internal_cc_common_get_tool_requirement_for_action,
        ),
        cc_internal = struct(
            actions2ctx_cheat = builtins_internal_cc_internal_actions2ctx_cheat,
            cc_toolchain_features = builtins_internal_cc_internal_cc_toolchain_features,
            cc_toolchain_variables = builtins_internal_cc_internal_cc_toolchain_variables,
            check_private_api = builtins_internal_cc_internal_check_private_api,
            collect_libraries_to_link = builtins_internal_cc_internal_collect_libraries_to_link,
            combine_cc_toolchain_variables = builtins_internal_cc_internal_combine_cc_toolchain_variables,
            compute_output_name_prefix_dir = builtins_internal_cc_internal_compute_output_name_prefix_dir,
            convert_library_to_link_list_to_linker_input_list = builtins_internal_cc_internal_convert_library_to_link_list_to_linker_input_list,
            create_cc_compile_action = builtins_internal_cc_internal_create_cc_compile_action,
            create_copts_filter = builtins_internal_cc_internal_create_copts_filter,
            create_cpp_source = builtins_internal_cc_internal_create_cpp_source,
            create_header_info = builtins_internal_cc_internal_create_header_info,
            create_header_info_with_deps = builtins_internal_cc_internal_create_header_info_with_deps,
            create_library_to_link = builtins_internal_cc_internal_create_library_to_link,
            create_module_map_action = builtins_internal_cc_internal_create_module_map_action,
            create_shared_non_lto_artifacts = builtins_internal_cc_internal_create_shared_non_lto_artifacts,
            declare_compile_output_file = builtins_internal_cc_internal_declare_compile_output_file,
            dynamic_library_soname = builtins_internal_cc_internal_dynamic_library_soname,
            dynamic_library_symlink = builtins_internal_cc_internal_dynamic_library_symlink,
            escape_label = builtins_internal_cc_internal_escape_label,
            exec_os = builtins_internal_cc_internal_exec_os,
            for_object_file = builtins_internal_cc_internal_for_object_file,
            for_static_library = builtins_internal_cc_internal_for_static_library,
            freeze = builtins_internal_cc_internal_freeze,
            get_artifact_name_for_category = builtins_internal_cc_internal_get_artifact_name_for_category,
            get_link_args = builtins_internal_cc_internal_get_link_args,
            intern_seq = builtins_internal_cc_internal_intern_seq,
            intern_string_sequence_variable_value = builtins_internal_cc_internal_intern_string_sequence_variable_value,
            is_tree_artifact = builtins_internal_cc_internal_is_tree_artifact,
            licenses = builtins_internal_cc_internal_licenses,
            per_file_copts = builtins_internal_cc_internal_per_file_copts,
            wrap_link_actions = builtins_internal_cc_internal_wrap_link_actions,
        ),
        java_common_internal_do_not_use = struct(
            _check_java_toolchain_is_declared_on_rule = builtins_internal_java_common_internal_do_not_use__check_java_toolchain_is_declared_on_rule,
            _google_legacy_api_enabled = builtins_internal_java_common_internal_do_not_use__google_legacy_api_enabled,
            _incompatible_java_info_merge_runtime_module_flags = builtins_internal_java_common_internal_do_not_use__incompatible_java_info_merge_runtime_module_flags,
            check_provider_instances = builtins_internal_java_common_internal_do_not_use_check_provider_instances,
            collect_native_deps_dirs = builtins_internal_java_common_internal_do_not_use_collect_native_deps_dirs,
            create_compilation_action = builtins_internal_java_common_internal_do_not_use_create_compilation_action,
            create_header_compilation_action = builtins_internal_java_common_internal_do_not_use_create_header_compilation_action,
            expand_java_opts = builtins_internal_java_common_internal_do_not_use_expand_java_opts,
            get_runtime_classpath_for_archive = builtins_internal_java_common_internal_do_not_use_get_runtime_classpath_for_archive,
            incompatible_disable_non_executable_java_binary = builtins_internal_java_common_internal_do_not_use_incompatible_disable_non_executable_java_binary,
            target_kind = builtins_internal_java_common_internal_do_not_use_target_kind,
            tokenize_javacopts = builtins_internal_java_common_internal_do_not_use_tokenize_javacopts,
            wrap_java_info = builtins_internal_java_common_internal_do_not_use_wrap_java_info,
        ),
        py_builtins = struct(
            are_action_listeners_enabled = builtins_internal_py_builtins_are_action_listeners_enabled,
            create_repo_mapping_manifest = builtins_internal_py_builtins_create_repo_mapping_manifest,
            get_current_os_name = builtins_internal_py_builtins_get_current_os_name,
            get_label_repo_runfiles_path = builtins_internal_py_builtins_get_label_repo_runfiles_path,
            get_legacy_external_runfiles = builtins_internal_py_builtins_get_legacy_external_runfiles,
            is_bzlmod_enabled = builtins_internal_py_builtins_is_bzlmod_enabled,
            make_runfiles_respect_legacy_external_runfiles = builtins_internal_py_builtins_make_runfiles_respect_legacy_external_runfiles,
            merge_runfiles_with_generated_inits_empty_files_supplier = builtins_internal_py_builtins_merge_runfiles_with_generated_inits_empty_files_supplier,
        ),
    ),
    toplevel = struct(
        native = struct(**exported_rules),
        **exported_toplevels
    ),
)
