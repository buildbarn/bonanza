package moduledotbazel

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"bonanza.build/pkg/label"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/starlark/unpack"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"
)

// ModuleExtensionProxy is called into by Parse() whenever
// module extension tags are declared, or if use_repo() is called
// against a given module extension.
type ModuleExtensionProxy interface {
	Tag(thread *eval.Thread, className string, attrs map[string]eval.Value) error
	UseRepo(repos map[label.ApparentRepo]label.ApparentRepo) error
}

type nullModuleExtensionProxy struct{}

func (nullModuleExtensionProxy) Tag(thread *eval.Thread, className string, attrs map[string]eval.Value) error {
	return nil
}

func (nullModuleExtensionProxy) UseRepo(repos map[label.ApparentRepo]label.ApparentRepo) error {
	return nil
}

// NullModuleExtensionProxy is an implementation of ModuleExtensionProxy
// that merely discards any tag declarations and use_repo() calls.
var NullModuleExtensionProxy ModuleExtensionProxy = nullModuleExtensionProxy{}

// RepoRuleProxy is called into by Parse() whenever a
// repository rule is invoked.
type RepoRuleProxy func(thread *eval.Thread, name label.ApparentRepo, devDependency bool, attrs map[string]eval.Value) error

// PatchOptions contains the common set of properties that are accepted
// by MODULE.bazel's archive_override(), git_override() and
// single_version_override().
type PatchOptions struct {
	Patches    []label.ApparentLabel
	PatchCmds  []string
	PatchStrip int
}

// RootHandler is called into by Parse() for
// each top-level declaration. It contains all methods that have an
// effect within the root module.
type RootHandler interface {
	ChildHandler

	RepositoryRuleOverride(thread *eval.Thread, moduleName label.Module, repositoryRuleIdentifier label.CanonicalStarlarkIdentifier, attrs map[string]eval.Value) error
	LocalPathOverride(moduleName label.Module, path path.Parser) error
	MultipleVersionOverride(moduleName label.Module, versions []label.ModuleVersion, registry *url.URL) error
	SingleVersionOverride(moduleName label.Module, version *label.ModuleVersion, registry *url.URL, patchOptions *PatchOptions) error
}

// ChildHandler contains the methods that may be called in
// MODULE.bazel, omitting any methods that should be ignored in child
// modules.
type ChildHandler interface {
	BazelDep(name label.Module, version *label.ModuleVersion, repoName label.ApparentRepo, devDependency bool) error
	Module(name label.Module, repoName label.ApparentRepo, bazelCompatibility []string) error
	RegisterExecutionPlatforms(platformTargetPatterns []label.ApparentTargetPattern, devDependency bool) error
	RegisterToolchains(toolchainTargetPatterns []label.ApparentTargetPattern, devDependency bool) error
	UseExtension(extensionBzlFile label.ApparentLabel, extensionName label.StarlarkIdentifier, devDependency, isolate bool) (ModuleExtensionProxy, error)
	UseRepoRule(repoRuleBzlFile label.ApparentLabel, repoRuleName label.StarlarkIdentifier) (RepoRuleProxy, error)
}

type overrideIgnoringRootHandler struct {
	ChildHandler
}

// NewOverrideIgnoringRootHandler wraps a
// ChildHandler, providing stubs for methods that only
// have an effect in the root module.
func NewOverrideIgnoringRootHandler(base ChildHandler) RootHandler {
	return &overrideIgnoringRootHandler{
		ChildHandler: base,
	}
}

func (overrideIgnoringRootHandler) LocalPathOverride(moduleName label.Module, path path.Parser) error {
	return nil
}

func (overrideIgnoringRootHandler) MultipleVersionOverride(moduleName label.Module, versions []label.ModuleVersion, registry *url.URL) error {
	return nil
}

func (overrideIgnoringRootHandler) RepositoryRuleOverride(thread *eval.Thread, moduleName label.Module, repositoryRuleIdentifier label.CanonicalStarlarkIdentifier, attrs map[string]eval.Value) error {
	return nil
}

func (overrideIgnoringRootHandler) SingleVersionOverride(moduleName label.Module, version *label.ModuleVersion, registry *url.URL, patchOptions *PatchOptions) error {
	return nil
}

type moduleExtensionProxyValue struct {
	name  label.StarlarkIdentifier
	proxy ModuleExtensionProxy
}

var _ eval.HasAttrs = &moduleExtensionProxyValue{}

func (moduleExtensionProxyValue) Type() string {
	return "module_extension_proxy"
}

func (moduleExtensionProxyValue) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (v *moduleExtensionProxyValue) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString(v.name.String())
	return nil
}

func (v *moduleExtensionProxyValue) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	return eval.NewBuiltin(name, func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
		if len(args.Positional) > 0 {
			return nil, errors.New("module tags do not take positional arguments")
		}
		attrs := make(map[string]eval.Value, len(args.Named))
		for _, arg := range args.Named {
			attrs[arg.Name] = arg.Value
		}
		return eval.None, v.proxy.Tag(thread, name, attrs)
	}), nil
}

func (moduleExtensionProxyValue) AttrNames(*eval.Thread) ([]string, error) {
	// We don't allow iteration over attributes, because that
	// prevents evaluation without analyzing module extensions up
	// front.
	return nil, nil
}

var (
	targetIdentifierHTTPArchive   = util.Must(label.NewCanonicalStarlarkIdentifier("@@bazel_tools+//tools/build_defs/repo:http.bzl%http_archive"))
	targetIdentifierGitRepository = util.Must(label.NewCanonicalStarlarkIdentifier("@@bazel_tools+//tools/build_defs/repo:git.bzl%git_repository"))
)

// newRepositoryRuleOverrideBuiltin returns the implementation of a
// function like archive_override() and git_override(), which override
// a module using a repository rule.
func newRepositoryRuleOverrideBuiltin(name string, targetIdentifier label.CanonicalStarlarkIdentifier, handler RootHandler) *eval.Builtin {
	return eval.NewBuiltin(name, func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
		if len(args.Positional) > 0 {
			return nil, fmt.Errorf("%s: got %d positional arguments, want 0", b.Name(), len(args.Positional))
		}

		var moduleName *label.Module
		attrs := map[string]eval.Value{}
		for _, arg := range args.Named {
			switch arg.Name {
			case "module_name":
				if err := unpack.Pointer(unpack.Module).UnpackInto(thread, arg.Value, &moduleName); err != nil {
					return nil, fmt.Errorf("%s: for parameter %s: %w", b.Name(), arg.Name, err)
				}
			default:
				attrs[arg.Name] = arg.Value
			}
		}
		if moduleName == nil {
			return nil, fmt.Errorf("%s: missing module_name argument", b.Name())
		}
		return eval.None, handler.RepositoryRuleOverride(
			thread,
			*moduleName,
			targetIdentifier,
			attrs,
		)
	})
}

// Parse parses a MODULE.bazel file, and calls into the provided
// RootHandler for every observed declaration.
func Parse(contents string, filename label.CanonicalLabel, localPathFormat path.Format, handler RootHandler) error {
	predeclared := map[string]eval.Value{
		"archive_override": newRepositoryRuleOverrideBuiltin("archive_override", targetIdentifierHTTPArchive, handler),
		"bazel_dep": eval.NewBuiltin("bazel_dep", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			var name label.Module
			var version *label.ModuleVersion
			maxCompatibilityLevel := -1
			var repoName *label.ApparentRepo
			devDependency := false
			if err := eval.UnpackArgs(
				thread, b.Name(), args,
				"name", unpack.Bind(&name, unpack.Module),
				"version?", unpack.Bind(&version, unpack.IfNonEmptyString(unpack.Pointer(unpack.ModuleVersion))),
				"max_compatibility_level?", unpack.Bind(&maxCompatibilityLevel, unpack.Int[int]()),
				"repo_name?", unpack.Bind(&repoName, unpack.IfNotNone(unpack.IfNonEmptyString(unpack.Pointer(unpack.ApparentRepo)))),
				"dev_dependency?", unpack.Bind(&devDependency, unpack.Bool),
			); err != nil {
				return nil, err
			}
			if repoName == nil {
				r := name.ToApparentRepo()
				repoName = &r
			}
			return eval.None, handler.BazelDep(
				name,
				version,
				*repoName,
				devDependency,
			)
		}),
		"git_override": newRepositoryRuleOverrideBuiltin("git_override", targetIdentifierGitRepository, handler),
		"include": eval.NewBuiltin("include", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			return nil, errors.New("include() is not permitted, as it prevents modules from being reused")
		}),
		"local_path_override": eval.NewBuiltin("local_path_override", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			var moduleName label.Module
			var path path.Parser
			var pathUnpacker eval.Unpacker
			if localPathFormat == nil {
				// Local path format is unknown. Only
				// validate that the provided path is
				// a string, and discard it.
				var discardedPath string
				pathUnpacker = unpack.Bind(&discardedPath, unpack.String)
			} else {
				pathUnpacker = unpack.Bind(&path, unpack.PathParser(localPathFormat))
			}
			if err := eval.UnpackArgs(
				thread, b.Name(), args,
				"module_name", unpack.Bind(&moduleName, unpack.Module),
				"path", pathUnpacker,
			); err != nil {
				return nil, err
			}
			return eval.None, handler.LocalPathOverride(
				moduleName,
				path,
			)
		}),
		"module": eval.NewBuiltin("module", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			var name label.Module
			var version *label.ModuleVersion
			compatibilityLevel := 0
			var repoName *label.ApparentRepo
			var bazelCompatibility []string
			if err := eval.UnpackArgs(
				thread, b.Name(), args,
				"name", unpack.Bind(&name, unpack.Module),
				"version?", unpack.Bind(&version, unpack.IfNonEmptyString(unpack.Pointer(unpack.ModuleVersion))),
				"compatibility_level?", unpack.Bind(&compatibilityLevel, unpack.Int[int]()),
				"repo_name?", unpack.Bind(&repoName, unpack.IfNonEmptyString(unpack.Pointer(unpack.ApparentRepo))),
				"bazel_compatibility?", unpack.Bind(&bazelCompatibility, unpack.List(unpack.String)),
			); err != nil {
				return nil, err
			}
			if repoName == nil {
				r := name.ToApparentRepo()
				repoName = &r
			}
			return eval.None, handler.Module(
				name,
				*repoName,
				bazelCompatibility,
			)
		}),
		"multiple_version_override": eval.NewBuiltin("multiple_version_override", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			var moduleName label.Module
			var versions []label.ModuleVersion
			var registry *url.URL
			if err := eval.UnpackArgs(
				thread, b.Name(), args,
				"module_name", unpack.Bind(&moduleName, unpack.Module),
				"versions", unpack.Bind(&versions, unpack.List(unpack.ModuleVersion)),
				"registry?", unpack.Bind(&registry, unpack.IfNonEmptyString(unpack.URL)),
			); err != nil {
				return nil, err
			}
			return eval.None, handler.MultipleVersionOverride(
				moduleName,
				versions,
				registry,
			)
		}),
		"register_execution_platforms": eval.NewBuiltin("register_execution_platforms", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			var platformTargetPatterns []label.ApparentTargetPattern
			if err := unpack.List(unpack.ApparentTargetPattern).UnpackInto(thread, eval.NewTuple(args.Positional), &platformTargetPatterns); err != nil {
				return nil, err
			}
			devDependency := false
			if err := eval.UnpackArgs(
				thread, b.Name(), eval.Args{Named: args.Named},
				"dev_dependency?", unpack.Bind(&devDependency, unpack.Bool),
			); err != nil {
				return nil, err
			}
			return eval.None, handler.RegisterExecutionPlatforms(
				platformTargetPatterns,
				devDependency,
			)
		}),
		"register_toolchains": eval.NewBuiltin("register_toolchains", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			var toolchainTargetPatterns []label.ApparentTargetPattern
			if err := unpack.List(unpack.ApparentTargetPattern).UnpackInto(thread, eval.NewTuple(args.Positional), &toolchainTargetPatterns); err != nil {
				return nil, err
			}
			devDependency := false
			if err := eval.UnpackArgs(
				thread, b.Name(), eval.Args{Named: args.Named},
				"dev_dependency?", unpack.Bind(&devDependency, unpack.Bool),
			); err != nil {
				return nil, err
			}
			return eval.None, handler.RegisterToolchains(
				toolchainTargetPatterns,
				devDependency,
			)
		}),
		"single_version_override": eval.NewBuiltin("single_version_override", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			var moduleName label.Module
			var version *label.ModuleVersion
			var registry *url.URL
			var patchOptions PatchOptions
			if err := eval.UnpackArgs(
				thread, b.Name(), args,
				"module_name", unpack.Bind(&moduleName, unpack.Module),
				"version?", unpack.Bind(&version, unpack.IfNonEmptyString(unpack.Pointer(unpack.ModuleVersion))),
				"registry?", unpack.Bind(&registry, unpack.IfNonEmptyString(unpack.URL)),
				"patches?", unpack.Bind(&patchOptions.Patches, unpack.List(unpack.ApparentLabel)),
				"patch_cmds?", unpack.Bind(&patchOptions.PatchCmds, unpack.List(unpack.String)),
				"patch_strip?", unpack.Bind(&patchOptions.PatchStrip, unpack.Int[int]()),
			); err != nil {
				return nil, err
			}
			return eval.None, handler.SingleVersionOverride(
				moduleName,
				version,
				registry,
				&patchOptions,
			)
		}),
		"use_extension": eval.NewBuiltin("use_extension", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			if len(args.Positional) > 2 {
				return nil, fmt.Errorf("%s: got %d positional arguments, want at most 2", b.Name(), len(args.Positional))
			}
			var extensionBzlFile label.ApparentLabel
			var extensionName label.StarlarkIdentifier
			devDependency := false
			isolate := false
			if err := eval.UnpackArgs(
				thread, b.Name(), args,
				"extension_bzl_file", unpack.Bind(&extensionBzlFile, unpack.ApparentLabel),
				"extension_name", unpack.Bind(&extensionName, unpack.StarlarkIdentifier),
				"dev_dependency?", unpack.Bind(&devDependency, unpack.Bool),
				"isolate?", unpack.Bind(&isolate, unpack.Bool),
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
		"use_repo": eval.NewBuiltin("use_repo", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			if len(args.Positional) < 1 {
				return nil, fmt.Errorf("%s: got %d positional arguments, want at least 1", b.Name(), len(args.Positional))
			}

			proxyValue := args.Positional[0]
			proxyObject, ok := proxyValue.(*moduleExtensionProxyValue)
			if !ok {
				return nil, fmt.Errorf("%s: for parameter 0: got %s, want module_extension_proxy", b.Name(), proxyValue.Type())
			}

			repos := map[label.ApparentRepo]label.ApparentRepo{}
			for i := 1; i < len(args.Positional); i++ {
				var repo label.ApparentRepo
				if err := unpack.ApparentRepo.UnpackInto(thread, args.Positional[i], &repo); err != nil {
					return nil, fmt.Errorf("%s: for parameter %d: %w", b.Name(), i, err)
				}
				repos[repo] = repo
			}

			for _, arg := range args.Named {
				var key, value label.ApparentRepo
				if err := unpack.ApparentRepo.UnpackInto(thread, eval.String(arg.Name), &key); err != nil {
					return nil, fmt.Errorf("%s: for parameter %s: %w", b.Name(), arg.Name, err)
				}
				if err := unpack.ApparentRepo.UnpackInto(thread, arg.Value, &value); err != nil {
					return nil, fmt.Errorf("%s: for parameter %s: %w", b.Name(), arg.Name, err)
				}
				if _, ok := repos[key]; ok {
					return nil, fmt.Errorf("%s: repository %s declared multiple times", b.Name(), arg.Name)
				}
				repos[key] = value
			}

			return eval.None, proxyObject.proxy.UseRepo(repos)
		}),
		"use_repo_rule": eval.NewBuiltin("use_repo_rule", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			var repoRuleBzlFile label.ApparentLabel
			var repoRuleName label.StarlarkIdentifier
			if err := eval.UnpackArgs(
				thread, b.Name(), args,
				"repo_rule_bzl_file", unpack.Bind(&repoRuleBzlFile, unpack.ApparentLabel),
				"repo_rule_name", unpack.Bind(&repoRuleName, unpack.StarlarkIdentifier),
			); err != nil {
				return nil, err
			}
			repoRuleProxy, err := handler.UseRepoRule(repoRuleBzlFile, repoRuleName)
			if err != nil {
				return nil, err
			}
			return eval.NewBuiltin(repoRuleName.String(), func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
				if len(args.Positional) > 0 {
					return nil, fmt.Errorf("%s: got %d positional arguments, want 0", b.Name(), len(args.Positional))
				}

				var name *label.ApparentRepo
				var devDependency bool
				attrs := map[string]eval.Value{}
				for _, arg := range args.Named {
					switch arg.Name {
					case "name":
						if err := unpack.Pointer(unpack.ApparentRepo).UnpackInto(thread, arg.Value, &name); err != nil {
							return nil, fmt.Errorf("%s: for parameter %s: %w", b.Name(), arg.Name, err)
						}
					case "dev_dependency":
						if err := unpack.Bool.UnpackInto(thread, arg.Value, &devDependency); err != nil {
							return nil, fmt.Errorf("%s: for parameter %s: %w", b.Name(), arg.Name, err)
						}
					default:
						attrs[arg.Name] = arg.Value
					}
				}

				if name == nil {
					return nil, fmt.Errorf("%s: missing name argument", b.Name())
				}
				return eval.None, repoRuleProxy(thread, *name, devDependency, attrs)
			}), nil
		}),
	}

	// Parse, resolve and compile the file.
	filenameStr := filename.String()
	f, err := syntax.Parse(filenameStr, []byte(contents), syntax.Options{})
	if err != nil {
		return err
	}
	universe := eval.Universe()
	m, err := resolve.File(
		f,
		&resolve.ModuleDotBazelDialect,
		func(name string) bool {
			_, ok := predeclared[name]
			return ok
		},
		func(name string) bool {
			_, ok := universe[name]
			return ok
		},
	)
	if err != nil {
		return err
	}
	program := compile.File(f, m)

	// Execute the file. The filename of the module needs to be a
	// canonical label, so that unpack.ApparentLabel can resolve
	// relative labels.
	thread := eval.NewThread(context.Background(), universe)
	thread.Print = func(_ *eval.Thread, msg string) error {
		// TODO: Provide logging sink.
		fmt.Println(msg)
		return nil
	}
	return eval.ExecFile(thread, program, eval.NewInMemoryModule(filenameStr, predeclared, nil))
}
