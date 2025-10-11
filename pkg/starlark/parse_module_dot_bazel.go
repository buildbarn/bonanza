package starlark

import (
	"errors"
	"fmt"
	"net/url"

	"bonanza.build/pkg/label"
	"bonanza.build/pkg/starlark/unpack"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"go.starlark.net/starlark"
)

// ModuleExtensionProxy is called into by ParseModuleDotBazel() whenever
// module extension tags are declared, or if use_repo() is called
// against a given module extension.
type ModuleExtensionProxy interface {
	Tag(className string, attrs map[string]starlark.Value) error
	UseRepo(repos map[label.ApparentRepo]label.ApparentRepo) error
}

type nullModuleExtensionProxy struct{}

func (nullModuleExtensionProxy) Tag(className string, attrs map[string]starlark.Value) error {
	return nil
}

func (nullModuleExtensionProxy) UseRepo(repos map[label.ApparentRepo]label.ApparentRepo) error {
	return nil
}

// NullModuleExtensionProxy is an implementation of ModuleExtensionProxy
// that merely discards any tag declarations and use_repo() calls.
var NullModuleExtensionProxy ModuleExtensionProxy = nullModuleExtensionProxy{}

// RepoRuleProxy is called into by ParseModuleDotBazel() whenever a
// repository rule is invoked.
type RepoRuleProxy func(name label.ApparentRepo, devDependency bool, attrs map[string]starlark.Value) error

// PatchOptions contains the common set of properties that are accepted
// by MODULE.bazel's archive_override(), git_override() and
// single_version_override().
type PatchOptions struct {
	Patches    []label.ApparentLabel
	PatchCmds  []string
	PatchStrip int
}

// RootModuleDotBazelHandler is called into by ParseModuleDotBazel() for
// each top-level declaration. It contains all methods that have an
// effect within the root module.
type RootModuleDotBazelHandler interface {
	ChildModuleDotBazelHandler

	RepositoryRuleOverride(moduleName label.Module, repositoryRuleIdentifier label.CanonicalStarlarkIdentifier, attrs map[string]starlark.Value) error
	LocalPathOverride(moduleName label.Module, path path.Parser) error
	MultipleVersionOverride(moduleName label.Module, versions []label.ModuleVersion, registry *url.URL) error
	SingleVersionOverride(moduleName label.Module, version *label.ModuleVersion, registry *url.URL, patchOptions *PatchOptions) error
}

// ChildModuleDotBazelHandler contains the methods that may be called in
// MODULE.bazel, omitting any methods that should be ignored in child
// modules.
type ChildModuleDotBazelHandler interface {
	BazelDep(name label.Module, version *label.ModuleVersion, maxCompatibilityLevel int, repoName label.ApparentRepo, devDependency bool) error
	Module(name label.Module, version *label.ModuleVersion, compatibilityLevel int, repoName label.ApparentRepo, bazelCompatibility []string) error
	RegisterExecutionPlatforms(platformTargetPatterns []label.ApparentTargetPattern, devDependency bool) error
	RegisterToolchains(toolchainTargetPatterns []label.ApparentTargetPattern, devDependency bool) error
	UseExtension(extensionBzlFile label.ApparentLabel, extensionName label.StarlarkIdentifier, devDependency, isolate bool) (ModuleExtensionProxy, error)
	UseRepoRule(repoRuleBzlFile label.ApparentLabel, repoRuleName label.StarlarkIdentifier) (RepoRuleProxy, error)
}

type overrideIgnoringRootModuleDotBazelHandler struct {
	ChildModuleDotBazelHandler
}

// NewOverrideIgnoringRootModuleDotBazelHandler wraps a
// ChildModuleDotBazelHandler, providing stubs for methods that only
// have an effect in the root module.
func NewOverrideIgnoringRootModuleDotBazelHandler(base ChildModuleDotBazelHandler) RootModuleDotBazelHandler {
	return &overrideIgnoringRootModuleDotBazelHandler{
		ChildModuleDotBazelHandler: base,
	}
}

func (overrideIgnoringRootModuleDotBazelHandler) LocalPathOverride(moduleName label.Module, path path.Parser) error {
	return nil
}

func (overrideIgnoringRootModuleDotBazelHandler) MultipleVersionOverride(moduleName label.Module, versions []label.ModuleVersion, registry *url.URL) error {
	return nil
}

func (overrideIgnoringRootModuleDotBazelHandler) RepositoryRuleOverride(moduleName label.Module, repositoryRuleIdentifier label.CanonicalStarlarkIdentifier, attrs map[string]starlark.Value) error {
	return nil
}

func (overrideIgnoringRootModuleDotBazelHandler) SingleVersionOverride(moduleName label.Module, version *label.ModuleVersion, registry *url.URL, patchOptions *PatchOptions) error {
	return nil
}

type moduleExtensionProxyValue struct {
	name  label.StarlarkIdentifier
	proxy ModuleExtensionProxy
}

var _ starlark.HasAttrs = &moduleExtensionProxyValue{}

func (v *moduleExtensionProxyValue) String() string {
	return v.name.String()
}

func (v *moduleExtensionProxyValue) Type() string {
	return "module_extension_proxy"
}

func (v *moduleExtensionProxyValue) Freeze() {}

func (v *moduleExtensionProxyValue) Truth() starlark.Bool {
	return starlark.True
}

func (v *moduleExtensionProxyValue) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("module_extension_proxy cannot be hashed")
}

func (v *moduleExtensionProxyValue) Attr(thread *starlark.Thread, name string) (starlark.Value, error) {
	return starlark.NewBuiltin(name, func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(args) > 0 {
			return nil, errors.New("module tags do not take positional arguments")
		}
		attrs := make(map[string]starlark.Value, len(kwargs))
		for _, kwarg := range kwargs {
			attrs[string(kwarg[0].(starlark.String))] = kwarg[1]
		}
		return starlark.None, v.proxy.Tag(name, attrs)
	}), nil
}

func (v *moduleExtensionProxyValue) AttrNames() []string {
	// We don't allow iteration over attributes, because that
	// prevents evaluation without analyzing module extensions up
	// front.
	return nil
}

var (
	targetIdentifierHTTPArchive   = util.Must(label.NewCanonicalStarlarkIdentifier("@@bazel_tools+//tools/build_defs/repo:http.bzl%http_archive"))
	targetIdentifierGitRepository = util.Must(label.NewCanonicalStarlarkIdentifier("@@bazel_tools+//tools/build_defs/repo:git.bzl%git_repository"))
)

// ParseModuleDotBazel parses a MODULE.bazel file, and call into
// ModuleDotBazelHandler for every observed declaration.
func ParseModuleDotBazel(contents string, filename label.CanonicalLabel, localPathFormat path.Format, handler RootModuleDotBazelHandler) error {
	repositoryRuleOverrideFunc := func(targetIdentifier label.CanonicalStarlarkIdentifier) func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		return func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			if len(args) > 0 {
				return nil, fmt.Errorf("%s: got %d positional arguments, want 0", b.Name(), len(args))
			}

			var moduleName *label.Module
			attrs := map[string]starlark.Value{}
			for _, kwarg := range kwargs {
				switch key := string(kwarg[0].(starlark.String)); key {
				case "module_name":
					if err := unpack.Pointer(unpack.Module).UnpackInto(thread, kwarg[1], &moduleName); err != nil {
						return nil, fmt.Errorf("%s: for parameter %s: %w", b.Name(), key, err)
					}
				default:
					attrs[key] = kwarg[1]
				}
			}
			if moduleName == nil {
				return nil, fmt.Errorf("%s: missing module_name argument", b.Name())
			}
			return starlark.None, handler.RepositoryRuleOverride(
				*moduleName,
				targetIdentifier,
				attrs,
			)
		}
	}

	_, err := starlark.ExecFile(
		&starlark.Thread{
			Name: "main",
			Print: func(_ *starlark.Thread, msg string) {
				// TODO: Provide logging sink.
				fmt.Println(msg)
			},
		},
		filename.String(),
		contents,
		starlark.StringDict{
			"archive_override": starlark.NewBuiltin("archive_override", repositoryRuleOverrideFunc(targetIdentifierHTTPArchive)),
			"bazel_dep": starlark.NewBuiltin("bazel_dep", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				var name label.Module
				var version *label.ModuleVersion
				maxCompatibilityLevel := -1
				var repoName *label.ApparentRepo
				devDependency := false
				if err := starlark.UnpackArgs(
					b.Name(), args, kwargs,
					"name", unpack.Bind(thread, &name, unpack.Module),
					"version?", unpack.Bind(thread, &version, unpack.IfNonEmptyString(unpack.Pointer(unpack.ModuleVersion))),
					"max_compatibility_level?", unpack.Bind(thread, &maxCompatibilityLevel, unpack.Int[int]()),
					"repo_name?", unpack.Bind(thread, &repoName, unpack.IfNonEmptyString(unpack.Pointer(unpack.ApparentRepo))),
					"dev_dependency?", unpack.Bind(thread, &devDependency, unpack.Bool),
				); err != nil {
					return nil, err
				}
				if repoName == nil {
					r := name.ToApparentRepo()
					repoName = &r
				}
				return starlark.None, handler.BazelDep(
					name,
					version,
					maxCompatibilityLevel,
					*repoName,
					devDependency,
				)
			}),
			"git_override": starlark.NewBuiltin("git_override", repositoryRuleOverrideFunc(targetIdentifierGitRepository)),
			"include": starlark.NewBuiltin("include", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				return nil, errors.New("include() is not permitted, as it prevents modules from being reused")
			}),
			"local_path_override": starlark.NewBuiltin("local_path_override", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				var moduleName label.Module
				var path path.Parser
				var pathUnpacker starlark.Unpacker
				if localPathFormat == nil {
					// Local path format is unknown. Only
					// validate that the provided path is
					// a string, and discard it.
					var discardedPath string
					pathUnpacker = unpack.Bind(thread, &discardedPath, unpack.String)
				} else {
					pathUnpacker = unpack.Bind(thread, &path, unpack.PathParser(localPathFormat))
				}
				if err := starlark.UnpackArgs(
					b.Name(), args, kwargs,
					"module_name", unpack.Bind(thread, &moduleName, unpack.Module),
					"path", pathUnpacker,
				); err != nil {
					return nil, err
				}
				return starlark.None, handler.LocalPathOverride(
					moduleName,
					path,
				)
			}),
			"module": starlark.NewBuiltin("module", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				var name label.Module
				var version *label.ModuleVersion
				compatibilityLevel := 0
				var repoName *label.ApparentRepo
				var bazelCompatibility []string
				if err := starlark.UnpackArgs(
					b.Name(), args, kwargs,
					"name", unpack.Bind(thread, &name, unpack.Module),
					"version?", unpack.Bind(thread, &version, unpack.IfNonEmptyString(unpack.Pointer(unpack.ModuleVersion))),
					"compatibility_level?", unpack.Bind(thread, &compatibilityLevel, unpack.Int[int]()),
					"repo_name?", unpack.Bind(thread, &repoName, unpack.IfNonEmptyString(unpack.Pointer(unpack.ApparentRepo))),
					"bazel_compatibility?", unpack.Bind(thread, &bazelCompatibility, unpack.List(unpack.String)),
				); err != nil {
					return nil, err
				}
				if repoName == nil {
					r := name.ToApparentRepo()
					repoName = &r
				}
				return starlark.None, handler.Module(
					name,
					version,
					compatibilityLevel,
					*repoName,
					bazelCompatibility,
				)
			}),
			"multiple_version_override": starlark.NewBuiltin("multiple_version_override", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				var moduleName label.Module
				var versions []label.ModuleVersion
				var registry *url.URL
				if err := starlark.UnpackArgs(
					b.Name(), args, kwargs,
					"module_name", unpack.Bind(thread, &moduleName, unpack.Module),
					"versions", unpack.Bind(thread, &versions, unpack.List(unpack.ModuleVersion)),
					"registry?", unpack.Bind(thread, &registry, unpack.IfNonEmptyString(unpack.URL)),
				); err != nil {
					return nil, err
				}
				return starlark.None, handler.MultipleVersionOverride(
					moduleName,
					versions,
					registry,
				)
			}),
			"register_execution_platforms": starlark.NewBuiltin("register_execution_platforms", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				var platformTargetPatterns []label.ApparentTargetPattern
				if err := unpack.List(unpack.ApparentTargetPattern).UnpackInto(thread, args, &platformTargetPatterns); err != nil {
					return nil, err
				}
				devDependency := false
				if err := starlark.UnpackArgs(
					b.Name(), nil, kwargs,
					"dev_dependency?", unpack.Bind(thread, &devDependency, unpack.Bool),
				); err != nil {
					return nil, err
				}
				return starlark.None, handler.RegisterExecutionPlatforms(
					platformTargetPatterns,
					devDependency,
				)
			}),
			"register_toolchains": starlark.NewBuiltin("register_toolchains", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				var toolchainTargetPatterns []label.ApparentTargetPattern
				if err := unpack.List(unpack.ApparentTargetPattern).UnpackInto(thread, args, &toolchainTargetPatterns); err != nil {
					return nil, err
				}
				devDependency := false
				if err := starlark.UnpackArgs(
					b.Name(), nil, kwargs,
					"dev_dependency?", unpack.Bind(thread, &devDependency, unpack.Bool),
				); err != nil {
					return nil, err
				}
				return starlark.None, handler.RegisterToolchains(
					toolchainTargetPatterns,
					devDependency,
				)
			}),
			"single_version_override": starlark.NewBuiltin("single_version_override", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				var moduleName label.Module
				var version *label.ModuleVersion
				var registry *url.URL
				var patchOptions PatchOptions
				if err := starlark.UnpackArgs(
					b.Name(), args, kwargs,
					"module_name", unpack.Bind(thread, &moduleName, unpack.Module),
					"version?", unpack.Bind(thread, &version, unpack.IfNonEmptyString(unpack.Pointer(unpack.ModuleVersion))),
					"registry?", unpack.Bind(thread, &registry, unpack.IfNonEmptyString(unpack.URL)),
					"patches?", unpack.Bind(thread, &patchOptions.Patches, unpack.List(unpack.ApparentLabel)),
					"patch_cmds?", unpack.Bind(thread, &patchOptions.PatchCmds, unpack.List(unpack.String)),
					"patch_strip?", unpack.Bind(thread, &patchOptions.PatchStrip, unpack.Int[int]()),
				); err != nil {
					return nil, err
				}
				return starlark.None, handler.SingleVersionOverride(
					moduleName,
					version,
					registry,
					&patchOptions,
				)
			}),
			"use_extension": starlark.NewBuiltin("use_extension", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				if len(args) > 2 {
					return nil, fmt.Errorf("%s: got %d positional arguments, want at most 2", b.Name(), len(args))
				}
				var extensionBzlFile label.ApparentLabel
				var extensionName label.StarlarkIdentifier
				devDependency := false
				isolate := false
				if err := starlark.UnpackArgs(
					b.Name(), args, kwargs,
					"extension_bzl_file", unpack.Bind(thread, &extensionBzlFile, unpack.ApparentLabel),
					"extension_name", unpack.Bind(thread, &extensionName, unpack.StarlarkIdentifier),
					"dev_dependency?", unpack.Bind(thread, &devDependency, unpack.Bool),
					"isolate?", unpack.Bind(thread, &isolate, unpack.Bool),
				); err != nil {
					return nil, err
				}
				moduleExtensionProxy, err := handler.UseExtension(
					extensionBzlFile,
					extensionName,
					devDependency,
					isolate,
				)
				if err != nil {
					return nil, err
				}
				return &moduleExtensionProxyValue{
					name:  extensionName,
					proxy: moduleExtensionProxy,
				}, nil
			}),
			"use_repo": starlark.NewBuiltin("use_repo", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				if len(args) < 1 {
					return nil, fmt.Errorf("%s: got %d positional arguments, want at least 1", b.Name(), len(args))
				}

				proxyValue := args.Index(0)
				proxyObject, ok := proxyValue.(*moduleExtensionProxyValue)
				if !ok {
					return nil, fmt.Errorf("%s: for parameter 0: got %s, want module_extension_proxy", b.Name(), proxyValue.Type())
				}

				repos := map[label.ApparentRepo]label.ApparentRepo{}
				for i := 1; i < len(args); i++ {
					var repo label.ApparentRepo
					if err := unpack.ApparentRepo.UnpackInto(thread, args[i], &repo); err != nil {
						return nil, fmt.Errorf("%s: for parameter %d: %w", b.Name(), i, err)
					}
					repos[repo] = repo
				}

				for _, kwarg := range kwargs {
					var key, value label.ApparentRepo
					if err := unpack.ApparentRepo.UnpackInto(thread, kwarg[0], &key); err != nil {
						return nil, fmt.Errorf("%s: for parameter %s: %w", b.Name(), kwarg[0].(starlark.String), err)
					}
					if err := unpack.ApparentRepo.UnpackInto(thread, kwarg[1], &value); err != nil {
						return nil, fmt.Errorf("%s: for parameter %s: %w", b.Name(), kwarg[0].(starlark.String), err)
					}
					if _, ok := repos[key]; ok {
						return nil, fmt.Errorf("%s: repository %s declared multiple times", b.Name(), kwarg[0].(starlark.String))
					}
					repos[key] = value
				}

				return starlark.None, proxyObject.proxy.UseRepo(repos)
			}),
			"use_repo_rule": starlark.NewBuiltin("use_repo_rule", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				var repoRuleBzlFile label.ApparentLabel
				var repoRuleName label.StarlarkIdentifier
				if err := starlark.UnpackArgs(
					b.Name(), args, kwargs,
					"repo_rule_bzl_file", unpack.Bind(thread, &repoRuleBzlFile, unpack.ApparentLabel),
					"repo_rule_name", unpack.Bind(thread, &repoRuleName, unpack.StarlarkIdentifier),
				); err != nil {
					return nil, err
				}
				repoRuleProxy, err := handler.UseRepoRule(repoRuleBzlFile, repoRuleName)
				if err != nil {
					return nil, err
				}
				return starlark.NewBuiltin(repoRuleName.String(), func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
					if len(args) > 0 {
						return nil, fmt.Errorf("%s: got %d positional arguments, want 0", b.Name(), len(args))
					}

					var name *label.ApparentRepo
					var devDependency bool
					attrs := map[string]starlark.Value{}
					for _, kwarg := range kwargs {
						switch key := string(kwarg[0].(starlark.String)); key {
						case "name":
							if err := unpack.Pointer(unpack.ApparentRepo).UnpackInto(thread, kwarg[1], &name); err != nil {
								return nil, fmt.Errorf("%s: for parameter %s: %w", b.Name(), key, err)
							}
						case "dev_dependency":
							if err := unpack.Bool.UnpackInto(thread, kwarg[1], &devDependency); err != nil {
								return nil, fmt.Errorf("%s: for parameter %s: %w", b.Name(), key, err)
							}
						default:
							attrs[key] = kwarg[1]
						}
					}

					if name == nil {
						return nil, fmt.Errorf("%s: missing name argument", b.Name())
					}
					return starlark.None, repoRuleProxy(*name, devDependency, attrs)
				}), nil
			}),
		},
	)
	return err
}
