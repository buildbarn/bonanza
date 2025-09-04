load("@bazel_tools//fragments:fragment_info.bzl", "FragmentInfo")
load(
    "//:exports.bzl",
    "PlatformInfo",
    "SymlinkEntry",
    "TemplateVariableInfo",
    "runfiles",
)

def _wrap_actions(actions, bin_dir, label):
    def actions_declare_shareable_artifact(path, artifact_root = None):
        if artifact_root and artifact_root != bin_dir:
            fail("artifact_root %s is not equal to bin_dir %s, which is not supported by this implementation" % (artifact_root, bin_dir))

        expected_path_prefix = label.workspace_root + "/"
        if label.package:
            expected_path_prefix += label.package
            expected_path_prefix += "/"
        if not path.startswith(expected_path_prefix):
            fail("path %s does not start with %s, which is not supported by this implementation" % (path, expected_path_prefix))

        return actions.declare_file(path.removeprefix(expected_path_prefix))

    def actions_run_shell(*, command, **kwargs):
        actions.run(
            executable = "/bin/bash",
            arguments = ["-c", command],
            **kwargs
        )

    actions_fields = {
        field: getattr(actions, field)
        for field in dir(actions)
    } | {
        "declare_shareable_artifact": actions_declare_shareable_artifact,
        "run_shell": actions_run_shell,
    }
    return struct(**actions_fields)

def _maybe_add_ctx_fragments(ctx_fields, fragments):
    if fragments:
        ctx_fields["fragments"] = struct(**{
            fragment.label.name: fragment[FragmentInfo]
            for fragment in fragments
        })

def _to_symlink_entry_depset(v):
    if type(v) != "depset":
        return depset([
            SymlinkEntry(
                path = path,
                target_file = target_file,
            )
            for path, target_file in v.items()
        ])
    return v

def _wrap_rule_ctx(ctx):
    def ctx_coverage_instrumented(target = None):
        return False

    def ctx_expand_location(input, targets = []):
        all_targets = {}
        for target in targets:
            executable = target.files_to_run.executable
            files = target.files.to_list()
            all_targets[target.label] = [executable] if executable and len(files) != 1 else files

        result = ""
        result_until_start_of_directive = ""
        state = 0
        directive = ""
        for c in input.elems():
            if c == "$":
                result_until_start_of_directive = result
                result += c
                state = 1
            elif c == "(" and state == 1:
                directive = ""
                result += c
                state = 2
            elif c == ")" and state == 2:
                if directive.startswith("execpath ") or directive.startswith("location "):
                    l = ctx.label.relative(directive[9:])
                    targets = all_targets[l]
                    if len(targets) != 1:
                        fail(directive, "expands to multiple files")
                    result = result_until_start_of_directive + targets[0].path
                else:
                    result += c
                state = 0
            else:
                directive += c
                result += c

        return result

    def ctx_runfiles(files = [], transitive_files = None, collect_data = False, collect_default = False, symlinks = {}, root_symlinks = {}):
        direct = runfiles(
            files = depset(direct = files, transitive = [transitive_files] if transitive_files else []),
            symlinks = _to_symlink_entry_depset(symlinks),
            root_symlinks = _to_symlink_entry_depset(root_symlinks),
        )
        if collect_data or collect_default:
            # TODO: Implement this feature!
            pass
        return direct

    ctx_fields = {
        field: getattr(ctx, field)
        for field in dir(ctx)
    } | {
        "actions": _wrap_actions(ctx.actions, ctx.bin_dir, ctx.label),
        "build_file_path": ctx.label.package + "/BUILD",
        "coverage_instrumented": ctx_coverage_instrumented,
        "disabled_features": [],
        "expand_location": ctx_expand_location,
        "features": [],
        "genfiles_dir": ctx.bin_dir,
        "runfiles": ctx_runfiles,
        "workspace_name": "_main",
    }

    # If the rule depends on one or more fragments, an attribute with
    # name "__fragments" of type attr.label_list() is injected. The
    # default value of this attribute will refer to targets offering a
    # FragmentInfo. Make these available through ctx.configuration and
    # ctx.fragments.
    if hasattr(ctx.attr, "__configuration"):
        ctx_fields["configuration"] = ctx.attr.__configuration[FragmentInfo]
    _maybe_add_ctx_fragments(ctx_fields, getattr(ctx.attr, "__fragments", []))

    # If the rule has a default exec group, expose its toolchains
    # through ctx.toolchains.
    if "" in ctx.exec_groups:
        ctx_fields["toolchains"] = ctx.exec_groups[""].toolchains

    # If the rule has attributes "__default_toolchains" and
    # "toolchains", we should add ctx.var containing all make variables
    # such as BINDIR and COMPILATION_MODE. With those variables in
    # place, we may also provide ctx.expand_make_variables().
    if hasattr(ctx.attr, "__default_toolchains"):
        var = {}
        for toolchain in ctx.attr.__default_toolchains:
            var |= toolchain[TemplateVariableInfo].variables
        for toolchain in ctx.attr.toolchains:
            var |= toolchain[TemplateVariableInfo].variables

        def ctx_expand_make_variables(attribute_name, command, additional_substitutions):
            def get_value(variable_name):
                if variable_name in additional_substitutions:
                    return additional_substitutions[variable_name]
                return var[variable_name]

            result = ""
            state = 0
            variable_name = ""
            for c in command.elems():
                if state == 0:
                    if c == "$":
                        state = 1
                    else:
                        result += c
                elif state == 1:
                    if c == "(":
                        state = 2
                    elif c == "$":
                        result += c
                        state = 0
                    else:
                        result += get_value(c)
                        state = 0
                elif state == 2:
                    if c == ")":
                        result += get_value(variable_name)
                        variable_name = ""
                        state = 0
                    else:
                        variable_name += c
                else:
                    fail("bad state")

            if state != 0:
                fail("command terminates in the middle of a $ sequence")
            return result

        ctx_fields["expand_make_variables"] = ctx_expand_make_variables
        ctx_fields["var"] = var

    # Even though most rules depend on the target platform in an
    # indirect way (e.g., through toolchain resolution), only rules that
    # call ctx.target_platform_has_constraint() depend on the actual
    # definition of the target platform.
    if hasattr(ctx.attr, "__target_platform"):
        def ctx_target_platform_has_constraint(constraintValue):
            return ctx.attr.__target_platform[PlatformInfo].constraints.get(
                constraintValue.constraint.label,
                constraintValue.constraint.default_constraint_value,
            ) == constraintValue.label

        ctx_fields["target_platform_has_constraint"] = ctx_target_platform_has_constraint

    return struct(**ctx_fields)

def invoke_rule(fn, ctx):
    return fn(_wrap_rule_ctx(ctx))

def _wrap_subrule_ctx(ctx, fragments):
    ctx_fields = {
        field: getattr(ctx, field)
        for field in dir(ctx)
    } | {
        "actions": _wrap_actions(ctx.actions, None, ctx.label),
    }

    _maybe_add_ctx_fragments(ctx_fields, fragments)

    return struct(**ctx_fields)

def invoke_subrule(fn, ctx, *args, __fragments = [], **kwargs):
    return fn(_wrap_subrule_ctx(ctx, __fragments), *args, **kwargs)
