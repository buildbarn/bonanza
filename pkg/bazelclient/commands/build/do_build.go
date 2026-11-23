package build

import (
	"context"
	"encoding"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"

	"bonanza.build/pkg/bazelclient/arguments"
	"bonanza.build/pkg/bazelclient/commands"
	"bonanza.build/pkg/bazelclient/embeddedmodules"
	"bonanza.build/pkg/bazelclient/formatted"
	"bonanza.build/pkg/bazelclient/logging"
	"bonanza.build/pkg/crypto"
	"bonanza.build/pkg/dotbazel"
	"bonanza.build/pkg/glob"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_format "bonanza.build/pkg/model/format"
	model_parser "bonanza.build/pkg/model/parser"
	"bonanza.build/pkg/model/render"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_encoding_pb "bonanza.build/pkg/proto/model/encoding"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	model_executewithstorage_pb "bonanza.build/pkg/proto/model/executewithstorage"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	remoteexecution_pb "bonanza.build/pkg/proto/remoteexecution"
	dag_pb "bonanza.build/pkg/proto/storage/dag"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/remoteexecution"
	"bonanza.build/pkg/storage/dag"
	dag_grpc "bonanza.build/pkg/storage/dag/grpc"
	"bonanza.build/pkg/storage/object"
	object_grpc "bonanza.build/pkg/storage/object/grpc"
	object_namespacemapping "bonanza.build/pkg/storage/object/namespacemapping"

	"github.com/buildbarn/bb-storage/pkg/eviction"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/security/advancedtls"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func newGRPCClient(endpoint string, commonFlags *arguments.CommonFlags) (*grpc.ClientConn, error) {
	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	var target string
	var clientCredentials credentials.TransportCredentials
	switch scheme := endpointURL.Scheme; scheme {
	case "grpc":
		target = endpointURL.Host
		clientCredentials = insecure.NewCredentials()
	case "grpcs":
		target = endpointURL.Host
		clientCredentials, err = advancedtls.NewClientCreds(&advancedtls.Options{})
		if err != nil {
			return nil, fmt.Errorf("failed to create TLS client credentials: %w", err)
		}
	case "unix":
		target = endpoint
		clientCredentials = insecure.NewCredentials()
	default:
		return nil, errors.New("scheme is not supported")
	}

	return grpc.NewClient(target, grpc.WithTransportCredentials(clientCredentials))
}

type localCapturableDirectoryOptions[TFile model_core.ReferenceMetadata] struct {
	fileParameters *model_filesystem.FileCreationParameters
	capturer       model_filesystem.FileMerkleTreeCapturer[TFile]
}

type localCapturableDirectory[TDirectory, TFile model_core.ReferenceMetadata] struct {
	filesystem.DirectoryCloser
	options *localCapturableDirectoryOptions[TFile]

	// Matcher for patterns provided to REPO.bazel's
	// ignore_directories(), positioned at the path of the current
	// directory. Nil if none of the patterns can match any of the
	// directory's descendants, so that matching can be skipped for
	// the entire subtree.
	ignoredDirectoriesMatcher *glob.Matcher

	// Number of components in the path of the current directory,
	// relative to the module's root directory. This is used to
	// detect symbolic links that escape the module's root
	// directory.
	depth uint32
}

func (d *localCapturableDirectory[TDirectory, TFile]) ReadDir() ([]filesystem.FileInfo, error) {
	entries, err := d.DirectoryCloser.ReadDir()
	if err != nil {
		return nil, err
	}
	keptEntries := entries[:0]
	for _, entry := range entries {
		name := entry.Name()
		switch entry.Type() {
		case filesystem.FileTypeDirectory:
			if ignored, _ := matchIgnoredDirectory(d.ignoredDirectoriesMatcher, name); ignored {
				continue
			}
		case filesystem.FileTypeSymlink:
			// Ignore convenience symlinks that are created
			// by Bazel in the workspace's root directory
			// (e.g., bazel-bin, bazel-out).
			if d.depth == 0 && strings.HasPrefix(name.String(), "bazel-") {
				continue
			}

			// Even though symlink escapement is also
			// computed as part of creating the directory's
			// Merkle tree, checking it here allows us to
			// report the name of the offending symlink.
			targetParser, err := d.DirectoryCloser.Readlink(name)
			if err != nil {
				return nil, util.StatusWrapf(err, "Failed to read target of symbolic link %#v", name.String())
			}
			escapementCounter := model_filesystem.NewEscapementCountingScopeWalker()
			if err := path.Resolve(targetParser, escapementCounter); err != nil {
				return nil, util.StatusWrapf(err, "Failed to resolve target of symbolic link %#v", name.String())
			}
			if l := escapementCounter.GetLevels(); l == nil || l.Value > d.depth {
				return nil, status.Errorf(codes.InvalidArgument, "Symbolic link %#v has a target that potentially escapes the module's root directory", name.String())
			}
		}
		keptEntries = append(keptEntries, entry)
	}
	return keptEntries, nil
}

func (d *localCapturableDirectory[TDirectory, TFile]) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[TDirectory], model_filesystem.CapturableDirectory[TDirectory, TFile], error) {
	child, err := d.DirectoryCloser.EnterDirectory(name)
	if err != nil {
		return nil, nil, err
	}
	_, childMatcher := matchIgnoredDirectory(d.ignoredDirectoriesMatcher, name)
	return nil, &localCapturableDirectory[TDirectory, TFile]{
		DirectoryCloser:           child,
		options:                   d.options,
		ignoredDirectoriesMatcher: childMatcher,
		depth:                     d.depth + 1,
	}, nil
}

func (d *localCapturableDirectory[TDirectory, TFile]) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[TFile], error) {
	f, err := d.OpenRead(name)
	if err != nil {
		return nil, err
	}
	return &localCapturableFile[TFile]{
		file:    f,
		options: d.options,
	}, nil
}

type localCapturedDirectory struct {
	filesystem.DirectoryCloser
}

func (d localCapturedDirectory) EnterCapturedDirectory(name path.Component) (model_filesystem.CapturedDirectory, error) {
	child, err := d.DirectoryCloser.EnterDirectory(name)
	if err != nil {
		return nil, err
	}
	return localCapturedDirectory{
		DirectoryCloser: child,
	}, nil
}

type localCapturableFile[TFile model_core.ReferenceMetadata] struct {
	file    filesystem.FileReader
	options *localCapturableDirectoryOptions[TFile]
}

func (f *localCapturableFile[TFile]) CreateFileMerkleTree(ctx context.Context) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, TFile], error) {
	defer f.Discard()
	return model_filesystem.CreateFileMerkleTree(
		ctx,
		f.options.fileParameters,
		io.NewSectionReader(f.file, 0, math.MaxInt64),
		f.options.capturer,
	)
}

func (f *localCapturableFile[TFile]) Discard() {
	f.file.Close()
	f.file = nil
}

// defaultOutputGroupName is the name of the output group that Bazel
// uses to refer to the files contained in DefaultInfo.files.
const defaultOutputGroupName = "default"

// getOutputSelection computes which outputs to build, based on the
// values of --output_groups. Like Bazel, groups prefixed by "+" or "-"
// are added to or removed from the default set of output groups. If at
// least one group is not prefixed, the default set of output groups is
// omitted. The default set contains the output group named "default",
// which refers to the files contained in DefaultInfo.files, and any
// output groups provided through --extra_default_output_groups.
//
// In addition to the outputs to build, the names of the output groups
// that were requested explicitly are returned, excluding "default" and
// the output groups that are part of the default set. These are used to
// implement --incompatible_fail_on_unknown_output_groups.
func getOutputSelection(outputGroupsArguments, extraDefaultOutputGroupsArguments []string) (*model_analysis_pb.OutputSelection, []string) {
	splitArguments := func(arguments []string) []string {
		var outputGroups []string
		for _, argument := range arguments {
			outputGroups = append(outputGroups, strings.FieldsFunc(argument, func(r rune) bool { return r == ',' })...)
		}
		return outputGroups
	}
	outputGroups := splitArguments(outputGroupsArguments)

	selected := map[string]struct{}{}
	if !slices.ContainsFunc(outputGroups, func(outputGroup string) bool {
		return !strings.HasPrefix(outputGroup, "+") && !strings.HasPrefix(outputGroup, "-")
	}) {
		selected[defaultOutputGroupName] = struct{}{}
		for _, outputGroup := range splitArguments(extraDefaultOutputGroupsArguments) {
			selected[outputGroup] = struct{}{}
		}
	}
	explicit := map[string]struct{}{}
	for _, outputGroup := range outputGroups {
		if name, ok := strings.CutPrefix(outputGroup, "-"); ok {
			delete(selected, name)
			delete(explicit, name)
		} else {
			name := strings.TrimPrefix(outputGroup, "+")
			selected[name] = struct{}{}
			explicit[name] = struct{}{}
		}
	}

	_, defaultInfoFiles := selected[defaultOutputGroupName]
	delete(selected, defaultOutputGroupName)
	delete(explicit, defaultOutputGroupName)
	return &model_analysis_pb.OutputSelection{
		DefaultInfoFiles: defaultInfoFiles,
		OutputGroups:     slices.Sorted(maps.Keys(selected)),
	}, slices.Sorted(maps.Keys(explicit))
}

// DoBuild implements the "bazel build" command, which builds a
// specified set of targets in the current workspace.
func DoBuild(args *arguments.BuildCommand, workspacePath path.Parser) {
	logger := logging.NewLoggerFromFlags(&args.CommonFlags)
	commands.ValidateInsideWorkspace(logger, "build", workspacePath)

	evaluation := Evaluate(logger, &args.CommonFlags, workspacePath, "build", func(currentPackage label.CanonicalPackage) proto.Message {
		// Convert the target patterns and tag filters to a
		// query. This allows target patterns prefixed with "-"
		// to exclude targets (e.g., "bazel build -- //...
		// -//foo/...").
		query, err := label.NewQueryFromTargetPatternArguments(
			args.Arguments,
			strings.Split(args.BuildFlags.BuildTagFilters, ","),
			currentPackage.AppendTargetPattern,
		)
		if err != nil {
			logger.Fatal(formatted.Textf("Invalid target patterns: %s", err))
		}

		// Determine the configurations for which to build. The Bazel
		// CLI only supports specifying build setting overrides and a
		// single list of platforms. However, there is no way to pick
		// different build setting overrides depending on the platform.
		commonBuildSettingOverrides := make([]*model_analysis_pb.BuildResult_Key_BuildSettingOverride, 0, len(args.BuildSettingOverrides))
		rootRepo := currentPackage.GetCanonicalRepo()
		for _, override := range args.BuildSettingOverrides {
			commonBuildSettingOverrides = append(
				commonBuildSettingOverrides,
				&model_analysis_pb.BuildResult_Key_BuildSettingOverride{
					Label: rootRepo.AppendLabel(override.Label).String(),
					Value: override.Value,
				},
			)
		}
		commonBuildSettingOverrides = append(
			commonBuildSettingOverrides,
			&model_analysis_pb.BuildResult_Key_BuildSettingOverride{
				Label: "@bazel_tools//command_line_option:host_platform",
				Value: args.BuildFlags.HostPlatform,
			},
		)

		// Like Bazel, the target platform defaults to the host
		// platform if --platforms is not provided.
		targetPlatforms := strings.FieldsFunc(args.BuildFlags.Platforms, func(r rune) bool { return r == ',' })
		if len(targetPlatforms) == 0 {
			targetPlatforms = []string{args.BuildFlags.HostPlatform}
		}
		configurations := make([]*model_analysis_pb.BuildResult_Key_Configuration, 0, len(targetPlatforms))
		for _, targetPlatform := range targetPlatforms {
			configurations = append(configurations, &model_analysis_pb.BuildResult_Key_Configuration{
				BuildSettingOverrides: append(
					[]*model_analysis_pb.BuildResult_Key_BuildSettingOverride{{
						Label: "@bazel_tools//command_line_option:platforms",
						Value: targetPlatform,
					}},
					commonBuildSettingOverrides...,
				),
			})
		}

		// Aspects to apply to the top-level targets. Like Bazel,
		// --aspects may be provided multiple times, and accepts
		// comma-separated lists. Labels of .bzl files are made
		// absolute, so that they can be resolved from the
		// perspective of the root module.
		var aspects []string
		for _, aspectsArgument := range args.BuildFlags.Aspects {
			for _, aspect := range strings.FieldsFunc(aspectsArgument, func(r rune) bool { return r == ',' }) {
				separator := strings.LastIndexByte(aspect, '%')
				if separator < 0 {
					logger.Fatal(formatted.Textf("Invalid aspect %#v: expected <bzl-file-label>%%<aspect_name>", aspect))
				}
				apparentLabel, err := currentPackage.AppendLabel(aspect[:separator])
				if err != nil {
					logger.Fatal(formatted.Textf("Invalid aspect %#v: %s", aspect, err))
				}
				aspects = append(aspects, apparentLabel.String()+aspect[separator:])
			}
		}
		aspectParameters := make([]*model_analysis_pb.BuildResult_Key_AspectParameter, 0, len(args.BuildFlags.AspectsParameters))
		for _, aspectParameter := range args.BuildFlags.AspectsParameters {
			name, value, ok := strings.Cut(aspectParameter, "=")
			if !ok {
				logger.Fatal(formatted.Textf("Invalid aspect parameter %#v: expected <param_name>=<param_value>", aspectParameter))
			}
			aspectParameters = append(aspectParameters, &model_analysis_pb.BuildResult_Key_AspectParameter{
				Name:  name,
				Value: value,
			})
		}

		outputSelection, explicitOutputGroups := getOutputSelection(args.BuildFlags.OutputGroups, args.BuildFlags.ExtraDefaultOutputGroups)
		var requiredOutputGroups []string
		if args.BuildFlags.IncompatibleFailOnUnknownOutputGroups {
			requiredOutputGroups = explicitOutputGroups
		}

		return &model_analysis_pb.BuildResult_Key{
			Query:                label.QueryToString(query),
			Configurations:       configurations,
			Aspects:              aspects,
			AspectParameters:     aspectParameters,
			Outputs:              outputSelection,
			RequiredOutputGroups: requiredOutputGroups,
		}
	})
	if outcomesLink, ok := evaluation.GetOutcomesLink(); ok {
		logger.Info(formatted.Join(formatted.Text("Got build results "), outcomesLink))
	}
}

// builtinsModuleNames contains the names of modules containing
// Starlark code that should be loaded implicitly. builtins_core
// provides DefaultInfo, filegroup(), platform(), etc. Bazel implements
// these as native types written in Java, while Bonanza implements them
// in plain Starlark. builtins_bzl provides cc_binary(), etc.
var builtinsModuleNames = []string{
	"builtins_core",
	"builtins_bzl",
}

// Starlark functions to invoke to wrap the execution of aspect, rule
// and subrule implementation functions. These are used to decorate ctx
// to contain fields that are either deprecated, or trivially
// implementable in pure Starlark (e.g., ctx.expand_make_variables(),
// ctx.genfiles_dir, ctx.workspace_name).
const (
	aspectImplementationWrapperIdentifier  = "@@builtins_core+//:wrappers.bzl%invoke_aspect"
	ruleImplementationWrapperIdentifier    = "@@builtins_core+//:wrappers.bzl%invoke_rule"
	subruleImplementationWrapperIdentifier = "@@builtins_core+//:wrappers.bzl%invoke_subrule"
)

// Evaluate the value of a key remotely, using the modules contained in
// the current workspace. This function provides the logic that is
// shared by commands like "bazel build" and "bazel query". If
// evaluation fails, the program is terminated.
//
// The key to evaluate is obtained by calling getRequestedKey, which is
// provided the canonical package corresponding to the current working
// directory. It can be used to resolve relative labels and target
// patterns.
func Evaluate(logger logging.Logger, commonFlags *arguments.CommonFlags, workspacePath path.Parser, commandName string, getRequestedKey func(currentPackage label.CanonicalPackage) proto.Message) *Evaluation {
	remoteCacheClient, err := newGRPCClient(commonFlags.RemoteCache, commonFlags)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to create gRPC client for --remote_cache=%#v: %s", commonFlags.RemoteCache, err))
	}

	// Determine the names and paths of all modules that are present
	// on the local system and need to be uploaded as part of the
	// build. First look for local_path_override() directives in
	// MODULE.bazel.
	workspaceDirectory, err := filesystem.NewLocalDirectory(workspacePath)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to open workspace directory: %s", err))
	}
	moduleDotBazelFile, err := workspaceDirectory.OpenRead(dotbazel.ModuleComponent)
	workspaceDirectory.Close()
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to open MODULE.bazel: %s", err))
	}
	moduleDotBazelContents, err := io.ReadAll(io.NewSectionReader(moduleDotBazelFile, 0, math.MaxInt64))
	moduleDotBazelFile.Close()
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to read MODULE.bazel: %s", err))
	}
	modulePaths := map[label.Module]path.Parser{}
	moduleDotBazelHandler := NewLocalPathExtractingModuleDotBazelHandler(modulePaths, workspacePath)
	if err := dotbazel.ParseModule(
		string(moduleDotBazelContents),
		util.Must(label.NewCanonicalLabel("@@main+//:MODULE.bazel")),
		path.LocalFormat,
		moduleDotBazelHandler,
	); err != nil {
		logger.Fatal(formatted.Textf("Failed to parse MODULE.bazel: %s", err))
	}
	rootModuleName, err := moduleDotBazelHandler.GetRootModuleName()
	if err != nil {
		logger.Fatal(formatted.Text(err.Error()))
	}

	// Augment results with modules provided to --override_module.
	for _, overrideModule := range commonFlags.OverrideModule {
		fields := strings.SplitN(overrideModule, "=", 2)
		if len(fields) != 2 {
			logger.Fatal(formatted.Text("Module overrides must use the format ${module_name}=${path}"))
		}
		moduleName, err := label.NewModule(fields[0])
		if err != nil {
			logger.Fatal(formatted.Textf("Invalid module name %#v: %s", fields[0], err))
		}
		modulePaths[moduleName] = path.LocalFormat.NewParser(fields[1])
	}

	// Modules that are embedded into bonanza_bazel, such as
	// bazel_tools and builtins_core, are uploaded as well. Allow
	// these to be replaced with copies stored on the local system,
	// which is useful when making changes to them.
	embeddedModuleEntries, err := fs.ReadDir(embeddedmodules.Modules, ".")
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to list embedded modules: %s", err))
	}
	embeddedModulePaths := map[label.Module]*path.Trace{}
	for _, entry := range embeddedModuleEntries {
		moduleName, err := label.NewModule(entry.Name())
		if err != nil {
			logger.Fatal(formatted.Textf("Invalid embedded module name %#v: %s", entry.Name(), err))
		}
		if _, ok := modulePaths[moduleName]; !ok {
			embeddedModulePaths[moduleName] = (*path.Trace)(nil).Append(path.MustNewComponent(entry.Name()))
		}
	}

	moduleNames := slices.AppendSeq(
		slices.Collect(maps.Keys(modulePaths)),
		maps.Keys(embeddedModulePaths),
	)
	slices.SortFunc(moduleNames, func(a, b label.Module) int {
		return strings.Compare(a.String(), b.String())
	})

	// Determine parameters for creating file and directory Merkle
	// trees. Parameters include minimum/maximum sizes of the
	// resulting objects, and whether they are compressed and
	// encrypted.
	referenceFormat := util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1))
	encryptionKeyBytes, err := base64.StdEncoding.DecodeString(commonFlags.RemoteEncryptionKey)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to base64 decode value of --remote_encryption_key: %s", err))
	}
	defaultEncoders := []*model_encoding_pb.BinaryEncoder{{
		Encoder: &model_encoding_pb.BinaryEncoder_Encrypting{
			Encrypting: &model_encoding_pb.EncryptingBinaryEncoder{
				EncryptionKey: encryptionKeyBytes,
			},
		},
	}}
	var chunkEncoders []*model_encoding_pb.BinaryEncoder
	if commonFlags.RemoteCacheCompression {
		chunkEncoders = append(chunkEncoders, &model_encoding_pb.BinaryEncoder{
			Encoder: &model_encoding_pb.BinaryEncoder_ZstdCompressing{
				ZstdCompressing: &emptypb.Empty{},
			},
		})
	}
	chunkEncoders = append(chunkEncoders, defaultEncoders...)

	directoryParametersMessage := &model_filesystem_pb.DirectoryCreationParameters{
		Access: &model_filesystem_pb.DirectoryAccessParameters{
			Encoders: defaultEncoders,
		},
		DirectoriesParameters: &model_chunking_pb.Parameters{
			MinimumSizeBytes: 8 * 1024,
			HorizonSizeBytes: 8 * 8 * 1024,
		},
	}
	// The configurations of encoders are generated by the client
	// itself, meaning there is no need to enforce a policy on them.
	binaryEncoderFactory := model_encoding.NewBinaryEncoderFactory(true)
	directoryParameters, err := model_filesystem.NewDirectoryCreationParametersFromProto(directoryParametersMessage, referenceFormat, binaryEncoderFactory)
	if err != nil {
		logger.Fatal(formatted.Textf("Invalid directory creation parameters: %s", err))
	}
	fileParametersMessage := &model_filesystem_pb.FileCreationParameters{
		Access: &model_filesystem_pb.FileAccessParameters{
			ChunkEncoders:            chunkEncoders,
			FileContentsListEncoders: defaultEncoders,
		},
		ChunkParameters: &model_chunking_pb.Parameters{
			MinimumSizeBytes: 64 * 1024,
			HorizonSizeBytes: 512 * 1024,
		},
		ChunkGearTableSeed: encryptionKeyBytes,
		FileContentsListParameters: &model_chunking_pb.Parameters{
			MinimumSizeBytes: 4 * 1024,
			HorizonSizeBytes: 8 * 4 * 1024,
		},
	}
	fileParameters, err := model_filesystem.NewFileCreationParametersFromProto(fileParametersMessage, referenceFormat, binaryEncoderFactory)
	if err != nil {
		logger.Fatal(formatted.Textf("Invalid file creation parameters: %s", err))
	}

	// Construct Merkle trees for all modules that need to be
	// uploaded to storage.
	logger.Info(formatted.Text("Scanning module sources"))
	ctx := context.Background()
	group, groupCtx := errgroup.WithContext(ctx)
	moduleRootDirectories := make([]model_filesystem.CapturedDirectory, 0, len(moduleNames))
	createdModuleRootDirectories := make([]model_filesystem.CreatedDirectory[model_core.CreatedObjectTree], len(moduleNames))
	createMerkleTreesConcurrency := semaphore.NewWeighted(int64(runtime.NumCPU()))
	group.Go(func() error {
		for i, moduleName := range moduleNames {
			fileOptions := &localCapturableDirectoryOptions[model_core.NoopReferenceMetadata]{
				fileParameters: fileParameters,
				capturer:       model_filesystem.NewSimpleFileMerkleTreeCapturer(model_core.DiscardingCreatedObjectCapturer),
			}
			if embeddedModulePath, ok := embeddedModulePaths[moduleName]; ok {
				moduleRootDirectory := &embeddedDirectory[model_core.CreatedObjectTree, model_core.NoopReferenceMetadata]{
					fsys:    embeddedmodules.Modules,
					path:    embeddedModulePath,
					options: fileOptions,
				}
				moduleRootDirectories = append(moduleRootDirectories, moduleRootDirectory)
				if err := model_filesystem.CreateDirectoryMerkleTree(
					groupCtx,
					createMerkleTreesConcurrency,
					group,
					directoryParameters,
					moduleRootDirectory,
					model_filesystem.FileDiscardingDirectoryMerkleTreeCapturer,
					&createdModuleRootDirectories[i],
				); err != nil {
					return util.StatusWrapf(err, "Failed to create directory Merkle tree for embedded module %#v", moduleName.String())
				}
				continue
			}

			modulePath := modulePaths[moduleName]
			moduleRootDirectory, err := filesystem.NewLocalDirectory(modulePath)
			if err != nil {
				return util.StatusWrapf(err, "Failed to open root directory of module %#v", moduleName.String())
			}
			moduleRootDirectories = append(moduleRootDirectories, localCapturedDirectory{
				DirectoryCloser: moduleRootDirectory,
			})
			ignoredDirectoriesNFA, err := getIgnoredDirectoriesNFA(groupCtx, moduleRootDirectory, moduleName)
			if err != nil {
				return util.StatusWrapf(err, "Failed to obtain ignored directories of module %#v", moduleName.String())
			}
			var ignoredDirectoriesMatcher glob.Matcher
			ignoredDirectoriesMatcher.Initialize(ignoredDirectoriesNFA)
			if err := model_filesystem.CreateDirectoryMerkleTree(
				groupCtx,
				createMerkleTreesConcurrency,
				group,
				directoryParameters,
				&localCapturableDirectory[model_core.CreatedObjectTree, model_core.NoopReferenceMetadata]{
					DirectoryCloser:           moduleRootDirectory,
					options:                   fileOptions,
					ignoredDirectoriesMatcher: &ignoredDirectoriesMatcher,
				},
				model_filesystem.FileDiscardingDirectoryMerkleTreeCapturer,
				&createdModuleRootDirectories[i],
			); err != nil {
				return util.StatusWrapf(err, "Failed to create directory Merkle tree for module %#v", moduleName.String())
			}
		}
		return nil
	})
	if err := group.Wait(); err != nil {
		logger.Fatal(formatted.Text(err.Error()))
	}

	fetcherPKIXPublicKey, err := base64.StdEncoding.DecodeString(commonFlags.RemoteExecutorFetcherPkixPublicKey)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to base64 decode --remote_executor_fetcher_pkix_public_key: %s", err))
	}

	// TODO: Take the current working directory into account, so
	// that any relative target patterns are resolved correctly.
	currentPackage := rootModuleName.ToModuleInstance(nil).GetBareCanonicalRepo().GetRootPackage()

	// Construct a BuildSpecification message that lists all the
	// modules and contains all of the flags to instruct what needs
	// to be built.
	buildSpecification := model_analysis_pb.BuildSpecification_Value{
		RootModuleName:                         rootModuleName.String(),
		DirectoryCreationParameters:            directoryParametersMessage,
		FileCreationParameters:                 fileParametersMessage,
		IgnoreRootModuleDevDependencies:        commonFlags.IgnoreDevDependency,
		BuiltinsModuleNames:                    builtinsModuleNames,
		RepoPlatformLabel:                      commonFlags.RepoPlatform,
		FetchPlatformPkixPublicKey:             fetcherPKIXPublicKey,
		ActionEncoders:                         defaultEncoders,
		AnalysisEncoders:                       defaultEncoders,
		StarlarkValueEncoders:                  defaultEncoders,
		AspectImplementationWrapperIdentifier:  aspectImplementationWrapperIdentifier,
		RuleImplementationWrapperIdentifier:    ruleImplementationWrapperIdentifier,
		SubruleImplementationWrapperIdentifier: subruleImplementationWrapperIdentifier,
	}
	switch commonFlags.LockfileMode {
	case arguments.LockfileMode_Off:
	case arguments.LockfileMode_Update:
		buildSpecification.UseLockfile = &model_analysis_pb.BuildSpecification_Value_UseLockfile{}
	case arguments.LockfileMode_Refresh:
		buildSpecification.UseLockfile = &model_analysis_pb.BuildSpecification_Value_UseLockfile{
			Error: true,
		}
	case arguments.LockfileMode_Error:
		buildSpecification.UseLockfile = &model_analysis_pb.BuildSpecification_Value_UseLockfile{
			MaximumCacheDuration: &durationpb.Duration{Seconds: 3600},
		}
	default:
		panic("unknown lockfile mode")
	}
	if len(commonFlags.Registry) > 0 {
		buildSpecification.ModuleRegistryUrls = commonFlags.Registry
	} else {
		buildSpecification.ModuleRegistryUrls = []string{"https://bcr.bazel.build/"}
	}
	buildSpecificationPatcher := model_core.NewReferenceMessagePatcher[dag.ObjectContentsWalker]()

	for i, moduleName := range moduleNames {
		createdRootDirectory := createdModuleRootDirectories[i]
		if l := createdRootDirectory.MaximumSymlinkEscapementLevels; l == nil || l.Value != 0 {
			logger.Fatal(formatted.Textf("Module %#v contains one or more symbolic links that potentially escape the module's root directory", moduleName.String()))
		}
		if createdRootDirectory.IsEmpty() {
			buildSpecification.Modules = append(
				buildSpecification.Modules,
				&model_analysis_pb.BuildSpecification_Value_Module{
					Name:                   moduleName.String(),
					RootDirectoryReference: createdRootDirectory.ToDirectoryReference(nil),
				},
			)
			continue
		}
		createdObject, err := model_core.MarshalAndEncodeDeterministic(
			createdRootDirectory.Message.ToMarshaler(model_format.NewProtoList[model_filesystem_pb.DirectoryEntry]()),
			referenceFormat,
			directoryParameters.GetEncoder(),
		)
		if err != nil {
			logger.Fatal(formatted.Textf("Failed to create root directory object for module %#v: %s", moduleName.String(), err))
		}

		createdObjectTree := model_core.CreatedObjectTree(createdObject.Value)
		decodingParameters := createdObject.GetDecodingParameters()
		buildSpecification.Modules = append(
			buildSpecification.Modules,
			&model_analysis_pb.BuildSpecification_Value_Module{
				Name: moduleName.String(),
				RootDirectoryReference: createdRootDirectory.ToDirectoryReference(
					&model_core_pb.DecodableReference{
						Reference: buildSpecificationPatcher.AddReference(
							model_core.MetadataEntry[dag.ObjectContentsWalker]{
								LocalReference: createdObject.Value.GetLocalReference(),
								Metadata: model_filesystem.NewCapturedDirectoryWalker(
									directoryParameters.DirectoryAccessParameters,
									fileParameters,
									moduleRootDirectories[i],
									&createdObjectTree,
									decodingParameters,
								),
							},
						),
						DecodingParameters: decodingParameters,
					},
				),
			},
		)
	}

	actionEncoder, err := binaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(
		defaultEncoders,
		uint32(referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to create action encoder: %s", err))
	}
	analysisEncoder, err := binaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(
		buildSpecification.AnalysisEncoders,
		uint32(referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to create analysis encoder: %s", err))
	}
	starlarkValueEncoder, err := binaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(
		buildSpecification.StarlarkValueEncoders,
		uint32(referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to create Starlark value encoder: %s", err))
	}

	// TODO: Should these be moved into special overrides?
	/*
		var invocationID uuid.UUID
		if v := commonFlags.InvocationId; v == "" {
			invocationID = util.Must(uuid.NewRandom())
		} else {
			invocationID, err = uuid.Parse(v)
			if err != nil {
				logger.Fatal(formatted.Textf("Invalid --invocation_id=%#v: %s", v, err))
			}
		}
		var buildRequestID uuid.UUID
		if v := commonFlags.BuildRequestId; v == "" {
			buildRequestID = util.Must(uuid.NewRandom())
		} else {
			buildRequestID, err = uuid.Parse(v)
			if err != nil {
				logger.Fatal(formatted.Textf("Invalid --build_request_id=%#v: %s", v, err))
			}
		}
	*/

	overrides, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[dag.ObjectContentsWalker]) (encoding.BinaryMarshaler, error) {
		buildSpecificationKey, err := model_core.MarshalTopLevelAny(
			model_core.NewSimpleTopLevelMessage[object.LocalReference](
				&model_analysis_pb.BuildSpecification_Key{},
			),
		)
		if err != nil {
			return nil, err
		}
		buildSpecificationKeyReference, err := model_core.ComputeTopLevelMessageReference(buildSpecificationKey, referenceFormat)
		if err != nil {
			return nil, err
		}

		buildSpecificationValue, err := model_core.MarshalOpaque(
			model_core.NewPatchedMessage(&buildSpecification, buildSpecificationPatcher).ToMarshaler(model_format.NewProto[model_analysis_pb.BuildSpecification_Value]()),
		)
		if err != nil {
			return nil, err
		}

		return model_format.NewProtoList[model_evaluation_pb.Evaluations]().NewMarshaler([]*model_evaluation_pb.Evaluations{{
			Level: &model_evaluation_pb.Evaluations_Leaf_{
				Leaf: &model_evaluation_pb.Evaluations_Leaf{
					KeyReference: buildSpecificationKeyReference.GetRawReference(),
					Graphlet: &model_evaluation_pb.Graphlet{
						Evaluation: &model_evaluation_pb.Graphlet_EvaluationInline{
							EvaluationInline: &model_evaluation_pb.Evaluation{
								Value: buildSpecificationValue.Merge(patcher),
							},
						},
					},
				},
			},
		}}), nil
	})
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to create overrides list message: %s", err))
	}
	createdOverrides, err := model_core.MarshalAndEncodeDeterministic(overrides, referenceFormat, actionEncoder)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to create overrides list object: %s", err))
	}

	// The evaluation key that is requested. Also create a marshaled
	// copy of it, as we need to embed that into the bonanza_browser
	// URL to link to the results.
	patchedRequestedKey := model_core.NewSimplePatchedMessage[dag.ObjectContentsWalker](
		getRequestedKey(currentPackage),
	)
	requestedKey, _ := patchedRequestedKey.SortAndSetReferences()
	requestedKeyAny, err := model_core.MarshalTopLevelAny(requestedKey)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to marshal requested key: %s", err))
	}
	marshaledRequestedKey, err := model_core.MarshalTopLevelMessage(requestedKeyAny)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to marshal requested key: %s", err))
	}

	// Construct an Action message.
	actionMessage, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[dag.ObjectContentsWalker]) (encoding.BinaryMarshaler, error) {
		overridesReference, err := patcher.CaptureAndAddDecodableReference(
			ctx,
			createdOverrides,
			model_core.WalkableCreatedObjectCapturer,
		)
		if err != nil {
			return nil, err
		}

		patchedRequestedKeyAny, err := model_core.MarshalAny(patchedRequestedKey)
		if err != nil {
			return nil, err
		}

		return model_format.NewProto[model_evaluation_pb.Action]().NewMarshaler(&model_evaluation_pb.Action{
			OverridesReference: overridesReference,
			RequestedKeys: []*model_evaluation_pb.Keys{{
				Level: &model_evaluation_pb.Keys_Leaf{
					Leaf: patchedRequestedKeyAny.Merge(patcher),
				},
			}},
			// Keep these parameters constant, so that the
			// graphlets that are returned remain comparable
			// across evaluations.
			ChunkingParameters: &model_chunking_pb.Parameters{
				MinimumSizeBytes: 8 * 1024,
				HorizonSizeBytes: 8 * 8 * 1024,
			},
		}), nil
	})
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to create action message: %s", err))
	}
	createdAction, err := model_core.MarshalAndEncodeDeterministic(actionMessage, referenceFormat, actionEncoder)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to create action object: %s", err))
	}

	logger.Info(formatted.Text("Uploading module sources"))
	instanceName, err := object.NewInstanceName(commonFlags.RemoteInstanceName)
	if err != nil {
		logger.Fatal(formatted.Textf("Invalid --remote_instance_name=%#v: %s", commonFlags.RemoteInstanceName, err))
	}
	actionReference := createdAction.Value.GetLocalReference()
	actionGlobalReference := instanceName.WithLocalReference(actionReference)
	dagUploader := dag_grpc.NewUploader(
		dag_pb.NewUploaderClient(remoteCacheClient),
		semaphore.NewWeighted(10),
		object.NewLimit(&object_pb.Limit{
			Count:     1000,
			SizeBytes: 1 << 20,
		}),
	)
	if err := dagUploader.UploadDAG(
		ctx,
		actionGlobalReference,
		dag.NewSimpleObjectContentsWalker(
			createdAction.Value.Contents,
			createdAction.Value.Metadata,
		),
	); err != nil {
		logger.Fatal(formatted.Textf("Failed to upload workspace directory: %s", err))
	}

	clientPrivateKeyData, err := os.ReadFile(commonFlags.RemoteExecutorClientPrivateKey)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to read --remote_executor_client_private_key=%#v: %s", commonFlags.RemoteExecutorClientPrivateKey, err))
	}
	clientPrivateKey, err := crypto.ParsePEMWithPKCS8ECDHPrivateKey(clientPrivateKeyData)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to parse --remote_executor_client_private_key=%#v: %s", commonFlags.RemoteExecutorClientPrivateKey, err))
	}

	clientCertificateChainData, err := os.ReadFile(commonFlags.RemoteExecutorClientCertificateChain)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to read --remote_executor_client_certificate_chain=%#v: %s", commonFlags.RemoteExecutorClientCertificateChain, err))
	}
	clientCertificateChain, err := crypto.ParsePEMWithCertificateChain(clientCertificateChainData)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to parse --remote_executor_client_certificate_chain=%#v: %s", commonFlags.RemoteExecutorClientCertificateChain, err))
	}

	remoteExecutorClient, err := newGRPCClient(commonFlags.RemoteExecutor, commonFlags)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to create gRPC client for --remote_executor=%#v: %s", commonFlags.RemoteExecutor, err))
	}
	builderClient := model_executewithstorage.NewNamespaceAddingClient(
		model_executewithstorage.NewProtoClient(
			remoteexecution.NewProtoClient[*model_executewithstorage_pb.Action, model_executewithstorage_pb.Event, model_executewithstorage_pb.Result](
				remoteexecution.NewRemoteClient(
					remoteexecution_pb.NewExecutionClient(remoteExecutorClient),
					clientPrivateKey,
					clientCertificateChain,
				),
			),
		),
		object.Namespace{
			InstanceName:    instanceName,
			ReferenceFormat: referenceFormat,
		},
	)

	builderPKIXPublicKey, err := base64.StdEncoding.DecodeString(commonFlags.RemoteExecutorBuilderPkixPublicKey)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to base64 decode --remote_executor_builder_pkix_public_key: %s", err))
	}
	builderECDHPublicKey, err := crypto.ParsePKIXECDHPublicKey(builderPKIXPublicKey)
	if err != nil {
		logger.Fatal(formatted.Textf("Failed to parse --remote_executor_builder_pkix_public_key: %s", err))
	}

	decodableActionReference := model_core.CopyDecodable(createdAction, actionReference)
	actionReferenceStr := model_core.DecodableLocalReferenceToString(decodableActionReference)
	actionLink := formatted.Text(actionReferenceStr)
	browserURL := commonFlags.BrowserUrl
	evaluationActionObjectFormat := model_format.NewProto[model_evaluation_pb.Action]().ToProto()
	evaluationActionPathComponents, _ := model_format.ToPath(evaluationActionObjectFormat)
	if browserURL != "" {
		if actionURL, err := url.JoinPath(
			browserURL,
			append(
				[]string{
					"object",
					instanceName.AsURLSafeComponent(),
					referenceFormat.ToProto().String(),
					actionReferenceStr,
				},
				evaluationActionPathComponents...,
			)...,
		); err == nil {
			actionLink = formatted.Link(actionURL, actionLink)
		}
	}

	parsedObjectPool := model_parser.NewParsedObjectPool(
		eviction.NewLRUSet[model_parser.ParsedObjectEvictionKey](),
		/* maximumCount = */ 1e3,
		/* maximumSizeBytes = */ 1e5,
	)
	parsedObjectPoolIngester := model_parser.NewParsedObjectPoolIngester[object.LocalReference](
		parsedObjectPool,
		model_parser.NewDownloadingObjectReader(
			object_namespacemapping.NewNamespaceAddingDownloader(
				object_grpc.NewDownloader(object_pb.NewDownloaderClient(remoteCacheClient)),
				instanceName,
			),
		),
	)

	messageFormatter := messageFormatter{
		renderer: render.Renderer{
			Pretty:          commonFlags.MessageFormat == arguments.MessageFormat_Pretty,
			ReferenceFormat: &referenceFormat,
		},
	}
	if browserURL != "" {
		if baseURL, err := url.JoinPath(
			browserURL,
			"object",
			instanceName.AsURLSafeComponent(),
			referenceFormat.ToProto().String(),
		); err == nil {
			messageFormatter.objectBaseURL = baseURL
		}
	}

	logger.Info(formatted.Join(formatted.Text("Performing "+commandName+" "), actionLink))
	var resultReference *model_core.Decodable[object.LocalReference]
	var errBuild error
	namespace := object.Namespace{
		InstanceName:    instanceName,
		ReferenceFormat: referenceFormat,
	}
	progressReader := model_parser.LookupParsedObjectReader(
		parsedObjectPoolIngester,
		model_parser.NewChainedObjectParser(
			model_parser.NewEncodedObjectParser[object.LocalReference](actionEncoder),
			model_parser.NewProtoObjectParser[object.LocalReference, model_evaluation_pb.Progress](),
		),
	)
	progressLinesWritten := 0
	for update := range builderClient.RunAction(
		context.Background(),
		builderECDHPublicKey,
		&model_executewithstorage.Action[object.LocalReference]{
			Reference: &decodableActionReference,
			Encoders:  defaultEncoders,
			Format:    evaluationActionObjectFormat,
		},
		&encryptedaction_pb.Action_AdditionalData{
			ExecutionTimeout: &durationpb.Duration{Seconds: 24 * 60 * 60},
		},
		&resultReference,
		&errBuild,
	) {
		if !update.HasEvent {
			continue
		}

		// The builder does not provide a progress reference if the
		// progress message is empty.
		progressReference := update.Event
		progress := model_core.NewSimpleMessage[object.LocalReference](&model_evaluation_pb.Progress{})
		if progressReference != nil {
			var err error
			progress, err = progressReader.ReadObject(context.Background(), *progressReference)
			if err != nil {
				logger.Fatal(formatted.Textf("Failed to read progress message: %s", err))
			}
		}
		logger.RemovePreviousLines(progressLinesWritten)
		progressLinesWritten = 0

		logger.Info(formatted.Textf(
			"🚧 %d   🚦 %d   🚗💨 %d   🏁 %d   📤 %d   🌍 %d",
			progress.Message.BlockedKeysCount,
			progress.Message.EvaluatableKeysCount,
			uint64(len(progress.Message.OldestEvaluatingKeys))+progress.Message.AdditionalEvaluatingKeysCount,
			progress.Message.EvaluatedKeysCount,
			progress.Message.UploadingKeysCount,
			progress.Message.CompletedKeysCount,
		))
		progressLinesWritten++

		// Determine how many currently evaluating keys we want
		// to display in the terminal. If we can't display all
		// of them (or if the server wasn't able to fit all of
		// them in the progress event), we'll display the number
		// not shown at the bottom of the screen.
		evaluatingKeysToDisplay := progress.Message.OldestEvaluatingKeys
		additionalEvaluatingKeysCount := progress.Message.AdditionalEvaluatingKeysCount
		// Progress messages are written to stderr, prefixed
		// with the logger's prefix for informational messages.
		// Prevent lines from wrapping, as that would cause
		// them to be removed incorrectly.
		terminalColumns, terminalLines, err := term.GetSize(int(os.Stderr.Fd()))
		if err != nil {
			terminalColumns, terminalLines = 80, 24
		}
		messageColumns := max(terminalColumns-utf8.RuneCountInString(logging.InfoPrefix), 0)
		maximumEvaluatingKeysToDisplay := terminalLines - progressLinesWritten - 1
		if additionalEvaluatingKeysCount > 0 || len(evaluatingKeysToDisplay) > maximumEvaluatingKeysToDisplay {
			maximumEvaluatingKeysToDisplay--
		}
		if maximumEvaluatingKeysToDisplay < 0 {
			maximumEvaluatingKeysToDisplay = 0
		}
		if len(evaluatingKeysToDisplay) > maximumEvaluatingKeysToDisplay {
			additionalEvaluatingKeysCount += uint64(len(evaluatingKeysToDisplay)) - uint64(maximumEvaluatingKeysToDisplay)
			evaluatingKeysToDisplay = evaluatingKeysToDisplay[:maximumEvaluatingKeysToDisplay]
		}

		longestType := 0
		for _, evaluatingKey := range progress.Message.OldestEvaluatingKeys {
			if l := len(getAbbreviatedTypeURL(evaluatingKey.Key.GetValue().GetTypeUrl())); longestType < l {
				longestType = l
			}
		}

		for _, evaluatingKey := range evaluatingKeysToDisplay {
			logger.Info(
				formatted.Truncate(
					messageColumns,
					formatEvaluatingKeyProgress(
						formatKey(
							namespace,
							model_core.Nested(progress, evaluatingKey.Key),
							&messageFormatter,
							browserURL,
							/* outcomesReference = */ nil,
							longestType,
						),
						evaluatingKey.Key.GetValue(),
						evaluatingKey.Progress,
						&messageFormatter,
						messageColumns,
					),
				),
			)
			progressLinesWritten++
		}

		if additionalEvaluatingKeysCount > 0 {
			logger.Info(formatted.Textf("  ... and %d more", additionalEvaluatingKeysCount))
			progressLinesWritten++
		}
	}
	if errBuild != nil {
		logger.Fatal(formatted.Textf("Failed to perform %s: %s", commandName, errBuild))
	}
	logger.RemovePreviousLines(progressLinesWritten)

	resultReader := model_parser.LookupParsedObjectReader(
		parsedObjectPoolIngester,
		model_parser.NewChainedObjectParser(
			model_parser.NewEncodedObjectParser[object.LocalReference](actionEncoder),
			model_parser.NewProtoObjectParser[object.LocalReference, model_evaluation_pb.Result](),
		),
	)

	// The builder does not return a result reference if the result
	// message is empty.
	result := model_core.NewSimpleMessage[object.LocalReference](&model_evaluation_pb.Result{})
	if resultReference != nil {
		var err error
		result, err = resultReader.ReadObject(context.Background(), *resultReference)
		if err != nil {
			logger.Fatal(formatted.Textf("Failed to read result message: %s", err))
		}
	}

	var outcomesReference *model_core.Decodable[object.LocalReference]
	if rm := result.Message.OutcomesReference; rm != nil {
		r, err := model_core.FlattenDecodableReference(model_core.Nested(result, rm))
		if err != nil {
			logger.Fatal(formatted.Textf("Invalid evaluations reference: %s", err))
		}
		outcomesReference = &r
	}

	if f := result.Message.Failure; f != nil {
		printStackTrace(namespace, model_core.Nested(result, f.StackTraceKeys), logger, &messageFormatter, browserURL, outcomesReference)
		logger.Fatal(formatted.Textf("Failed to perform %s: %s", commandName, status.FromProto(f.Status)))
	}

	return &Evaluation{
		namespace:                namespace,
		browserURL:               browserURL,
		referenceFormat:          referenceFormat,
		parsedObjectPoolIngester: parsedObjectPoolIngester,
		actionEncoder:            actionEncoder,
		analysisEncoder:          analysisEncoder,
		starlarkValueEncoder:     starlarkValueEncoder,
		requestedKey:             requestedKeyAny,
		marshaledRequestedKey:    marshaledRequestedKey,
		outcomesReference:        outcomesReference,
	}
}

// formatMessageDescription converts a human readable description of a
// message to formatted text, emphasizing values of fields.
func formatMessageDescription(description model_evaluation.MessageDescription) formatted.Node {
	parts := make([]formatted.Node, 0, len(description.Segments))
	for _, segment := range description.Segments {
		if segment.IsField {
			parts = append(parts, formatted.Bold(formatted.Text(segment.Text)))
		} else {
			parts = append(parts, formatted.Text(segment.Text))
		}
	}
	return formatted.Join(parts...)
}

// formatEvaluatingKeyProgress appends the progress reported by the
// evaluation function of a key to the key's description. If the
// progress contains a counter, the line is rendered as a progress bar.
// Columns is the width of the terminal available to the line.
func formatEvaluatingKeyProgress(line formatted.Node, key *anypb.Any, marshaledProgress []byte, messageFormatter *messageFormatter, columns int) formatted.Node {
	if marshaledProgress == nil || !messageFormatter.renderer.Pretty {
		return line
	}
	progressMessageType, err := model_evaluation.GetProgressMessageType(key.MessageName())
	if err != nil {
		return line
	}
	progress, err := model_format.NewDynamicProto(progressMessageType).Unmarshal(marshaledProgress)
	if err != nil {
		return line
	}
	description, ok, err := model_evaluation.DescribeProgress(progress.ProtoReflect())
	if err != nil || !ok {
		return line
	}
	// Right align the progress, so that it is displayed at a
	// consistent location. If the line is too long to fit both the
	// key and the progress, truncate the key, as the progress is
	// what changes over time.
	const minimumPadding = 2
	progressNode := formatMessageDescription(description)
	progressWidth := plainTextWidth(progressNode)
	padding := columns - plainTextWidth(line) - progressWidth
	if padding < minimumPadding {
		line = formatted.Truncate(columns-progressWidth-minimumPadding, line)
		padding = minimumPadding
	}
	line = formatted.Join(line, formatted.Text(strings.Repeat(" ", padding)), progressNode)
	if counter := description.Counter; counter != nil && counter.Total > 0 {
		current := min(counter.Current, counter.Total)
		line = formatted.ProgressBar(int(uint64(columns)*current/counter.Total), line)
	}
	return line
}

// plainTextWidth returns the number of cells needed to display a node
// of formatted text in a terminal, assuming each rune occupies a single
// cell.
func plainTextWidth(n formatted.Node) int {
	var sb strings.Builder
	formatted.WritePlainText(n, &sb)
	return utf8.RuneCountInString(sb.String())
}

// formatKey formats an evaluation key on a single line, prefixed with
// its abbreviated type.
func formatKey(namespace object.Namespace, keyAny model_core.Message[*model_core_pb.Any, object.LocalReference], messageFormatter *messageFormatter, browserURL string, outcomesReference *model_core.Decodable[object.LocalReference], longestType int) formatted.Node {
	abbreviatedType := getAbbreviatedTypeURL(keyAny.Message.GetValue().GetTypeUrl())
	abbreviatedTypeNode := formatted.Text(abbreviatedType)
	var body formatted.Node
	if flattenedKey, err := model_core.FlattenAny(keyAny); err != nil {
		body = formatted.Bold(formatted.Text(fmt.Sprintf("Failed to flatten key: %s", err)))
	} else if key, err := model_core.UnmarshalTopLevelAnyNew[object.LocalReference](flattenedKey); err != nil {
		body = formatted.Bold(formatted.Text(fmt.Sprintf("Failed to unmarshal key: %s", err)))
	} else {
		if browserURL != "" && outcomesReference != nil {
			if marshaledKey, err := model_core.MarshalTopLevelMessage(flattenedKey); err == nil {
				if evaluationURL, err := url.JoinPath(
					browserURL,
					"evaluation",
					namespace.InstanceName.AsURLSafeComponent(),
					namespace.ReferenceFormat.ToProto().String(),
					model_core.DecodableLocalReferenceToString(*outcomesReference),
					base64.RawURLEncoding.EncodeToString(marshaledKey),
				); err == nil {
					abbreviatedTypeNode = formatted.Link(evaluationURL, abbreviatedTypeNode)
				}
			}
		}
		if description, ok, err := model_evaluation.DescribeKey(key.Message.ProtoReflect()); messageFormatter.renderer.Pretty && err == nil && ok {
			body = formatMessageDescription(description)
		} else {
			body = messageFormatter.formatSingleLine(
				messageFormatter.message(model_core.Nested(key.Decay(), key.Message.ProtoReflect())),
			)
		}
	}
	return formatted.Join(
		formatted.Text("  "),
		abbreviatedTypeNode,
		formatted.Textf("%*s", longestType-len(abbreviatedType), ""),
		formatted.Textf("  "),
		body,
	)
}

func printStackTrace(namespace object.Namespace, stackTraceKeys model_core.Message[[]*model_core_pb.Any, object.LocalReference], logger logging.Logger, messageFormatter *messageFormatter, browserURL string, outcomesReference *model_core.Decodable[object.LocalReference]) {
	if len(stackTraceKeys.Message) > 0 {
		logger.Error(formatted.Text("Traceback (most recent key last):"))

		longestType := 0
		for _, keyAny := range stackTraceKeys.Message {
			if l := len(getAbbreviatedTypeURL(keyAny.Value.GetTypeUrl())); longestType < l {
				longestType = l
			}
		}

		for _, keyAny := range stackTraceKeys.Message {
			logger.Error(
				formatKey(
					namespace,
					model_core.Nested(stackTraceKeys, keyAny),
					messageFormatter,
					browserURL,
					outcomesReference,
					longestType,
				),
			)
		}
	}
}

func getAbbreviatedTypeURL(typeURL string) string {
	typeURL = strings.TrimSuffix(typeURL, ".Key")
	if dot := strings.LastIndexByte(typeURL, '.'); dot >= 0 {
		return typeURL[dot+1:]
	}
	return typeURL
}
