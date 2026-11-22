load("@bazel_tools//fragments:fragment_info.bzl", "FragmentInfo")
load(
    "//:exports.bzl",
    "DefaultInfo",
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

    def actions_run_shell(*, command, arguments = [], **kwargs):
        actions.run(
            executable = "/bin/bash",
            arguments = ["-c", command, ""] + arguments,
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

def _expand_make_variables(command, get_value):
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

def _get_make_variables(attr, is_rule):
    """Returns the make variables of a rule or aspect.

    Make variables such as BINDIR and COMPILATION_MODE are provided by
    the targets in the "__default_toolchains" attribute that is injected
    into rules and aspects. For rules, the targets in the "toolchains"
    attribute are taken into account as well. Aspects don't have such an
    attribute, as public attributes of aspects are parameters. If the
    rule or aspect has no "__default_toolchains" attribute, None is
    returned.
    """
    if not hasattr(attr, "__default_toolchains"):
        return None
    var = {}
    for toolchain in attr.__default_toolchains:
        var |= toolchain[TemplateVariableInfo].variables
    if is_rule:
        for toolchain in getattr(attr, "toolchains", []):
            var |= toolchain[TemplateVariableInfo].variables
    return var

def _add_common_ctx_fields(ctx_fields, ctx, is_rule):
    """Adds fields to ctx that are shared by rules and aspects."""

    # TODO: Implement coverage instrumentation.
    def ctx_coverage_instrumented(target = None):
        return False

    ctx_fields["coverage_instrumented"] = ctx_coverage_instrumented

    # If the rule or aspect depends on one or more fragments, an
    # attribute with name "__fragments" of type attr.label_list() is
    # injected. The default value of this attribute will refer to
    # targets offering a FragmentInfo. Make these available through
    # ctx.configuration and ctx.fragments.
    if hasattr(ctx.attr, "__configuration"):
        ctx_fields["configuration"] = ctx.attr.__configuration[FragmentInfo]
    _maybe_add_ctx_fragments(ctx_fields, getattr(ctx.attr, "__fragments", []))

    # If the rule or aspect has a default exec group, expose its
    # toolchains through ctx.toolchains.
    if "" in ctx.exec_groups:
        ctx_fields["toolchains"] = ctx.exec_groups[""].toolchains

    # If the rule or aspect has an attribute "__default_toolchains", we
    # should add ctx.var containing all make variables such as BINDIR
    # and COMPILATION_MODE. With those variables in place, we may also
    # provide ctx.expand_make_variables().
    var = _get_make_variables(ctx.attr, is_rule)
    if var != None:
        def ctx_expand_make_variables(attribute_name, command, additional_substitutions):
            def get_value(variable_name):
                if variable_name in additional_substitutions:
                    return additional_substitutions[variable_name]
                return var[variable_name]

            return _expand_make_variables(command, get_value)

        ctx_fields["expand_make_variables"] = ctx_expand_make_variables
        ctx_fields["var"] = var

    # Even though most rules and aspects depend on the target platform
    # in an indirect way (e.g., through toolchain resolution), only the
    # ones that call ctx.target_platform_has_constraint() depend on the
    # actual definition of the target platform.
    if hasattr(ctx.attr, "__target_platform"):
        def ctx_target_platform_has_constraint(constraintValue):
            return ctx.attr.__target_platform[PlatformInfo].constraints.get(
                constraintValue.constraint.label,
                constraintValue.constraint.default_constraint_value,
            ) == constraintValue.label

        ctx_fields["target_platform_has_constraint"] = ctx_target_platform_has_constraint

def _file_execpath(f):
    return f.path

def _file_rootpath(f):
    return f.short_path

def _file_rlocationpath(f):
    # Files in external repositories have a short path of the form
    # "../repo/path", while files in the main repository have a short
    # path relative to the root of the main repository.
    short_path = f.short_path
    if short_path.startswith("../"):
        return short_path[3:]
    return "_main/" + short_path

# Location functions supported by $(...) directives, mapping to a
# function that computes the path of a file, and whether the directive
# may expand to multiple files. ctx.expand_location() and
# ctx.resolve_command() expand $(location) to exec paths.
_LOCATION_FUNCTIONS = {
    "execpath": (_file_execpath, False),
    "execpaths": (_file_execpath, True),
    "location": (_file_execpath, False),
    "locations": (_file_execpath, True),
    "rlocationpath": (_file_rlocationpath, False),
    "rlocationpaths": (_file_rlocationpath, True),
    "rootpath": (_file_rootpath, False),
    "rootpaths": (_file_rootpath, True),
}

def _expand_location_directives(input, expand_directive):
    """Replaces $(...) directives in a string.

    expand_directive is called with the contents of each $(...)
    directive. It returns the replacement string, or None if the
    directive needs to be left alone (e.g., because it is a make
    variable). Escaped dollar signs ("$$") are preserved, so that they
    can be processed by make variable expansion later on.
    """
    parts = []
    pos = 0
    for _ in range(len(input)):
        start = input.find("$", pos)
        if start < 0:
            break
        if input[start + 1:start + 2] == "(":
            end = input.find(")", start + 2)
            if end < 0:
                break
            replacement = expand_directive(input[start + 2:end])
            parts.append(input[pos:start] if replacement != None else input[pos:end + 1])
            if replacement != None:
                parts.append(replacement)
            pos = end + 1
        else:
            parts.append(input[pos:start + 2])
            pos = start + 2
    parts.append(input[pos:])
    return "".join(parts)

def _wrap_rule_ctx(ctx):
    def _expand_location(input, targets = [], label_dict = {}):
        # Map labels to the files they expand to, and the executable to
        # use if the label expands to multiple files in a context
        # where only a single file is permitted.
        all_targets = {
            label: (files, None)
            for label, files in label_dict.items()
        }
        for target in targets:
            all_targets[target.original_label] = (
                target.files.to_list(),
                target.files_to_run.executable,
            )
        for output_name in dir(ctx.outputs):
            output = getattr(ctx.outputs, output_name)
            if type(output) == type([]):
                for o in output:
                    all_targets[o.label] = ([o], None)
            else:
                all_targets[output.label] = ([output], None)

        def expand_directive(directive):
            function_name, separator, label_str = directive.partition(" ")
            if not separator or function_name not in _LOCATION_FUNCTIONS:
                return None
            get_path, multiple = _LOCATION_FUNCTIONS[function_name]

            label = ctx.label.relative(label_str.strip())
            if label not in all_targets:
                fail("label %s in $(%s) is not declared as a prerequisite of this rule" % (label, directive))
            files, executable = all_targets[label]

            if multiple:
                if not files and executable:
                    files = [executable]
                if not files:
                    fail("label %s in $(%s) expression expands to no files" % (label, directive))
                return " ".join(sorted([get_path(f) for f in files]))

            if executable and len(files) != 1:
                files = [executable]
            if len(files) != 1:
                fail("label %s in $(%s) expression expands to %d files, please use $(%ss %s) instead" % (label, directive, len(files), function_name, label_str))
            return get_path(files[0])

        return _expand_location_directives(input, expand_directive)

    def ctx_expand_location(input, targets = []):
        return _expand_location(input, targets)

    def ctx_resolve_command(command = "", attribute = None, expand_locations = False, make_variables = None, tools = [], label_dict = {}, execution_requirements = {}):
        if expand_locations:
            command = _expand_location(command, tools, label_dict)
        if make_variables != None:
            command = _expand_make_variables(command, lambda variable_name: make_variables[variable_name])

        # Tools need to be provided to the action as inputs, including
        # their runfiles.
        resolved_inputs = depset(transitive = [
            f
            for tool in tools
            for f in [tool.files, tool[DefaultInfo].default_runfiles.files]
        ]).to_list()
        return resolved_inputs, ["/bin/bash", "-c", command], []

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
        "expand_location": ctx_expand_location,
        "genfiles_dir": ctx.bin_dir,
        "resolve_command": ctx_resolve_command,
        "runfiles": ctx_runfiles,
        "workspace_name": "_main",
    }

    _add_common_ctx_fields(ctx_fields, ctx, is_rule = True)
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

def _wrap_aspect_ctx(ctx):
    ctx_fields = {
        field: getattr(ctx, field)
        for field in dir(ctx)
    } | {
        "actions": _wrap_actions(ctx.actions, ctx.bin_dir, ctx.label),
        "build_file_path": ctx.label.package + "/BUILD",
        "genfiles_dir": ctx.bin_dir,
        "workspace_name": "_main",
    }
    _add_common_ctx_fields(ctx_fields, ctx, is_rule = False)

    # Provide ctx.rule.var, containing the make variables of the rule
    # target to which the aspect is applied.
    rule_var = _get_make_variables(ctx.rule.attr, is_rule = True)
    if rule_var != None:
        ctx_fields["rule"] = struct(**({
            field: getattr(ctx.rule, field)
            for field in dir(ctx.rule)
        } | {
            "var": rule_var,
        }))

    return struct(**ctx_fields)

def invoke_aspect(fn, target, ctx):
    return fn(target, _wrap_aspect_ctx(ctx))
