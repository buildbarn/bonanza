package analysis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/proto"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// getReferenceEqualIdentifierGenerator creates an identifier generator
// for Starlark values that provide reference equality, such as depsets.
//
// As these identifiers end up getting written to storage, we want to
// use a deterministic process. We therefore hand out these identifiers
// sequentially, using a cryptographic hash of the current key as the
// initial offset.
func (c *baseComputer[TReference, TMetadata]) getReferenceEqualIdentifierGenerator(key model_core.Message[proto.Message, TReference]) (model_starlark.ReferenceEqualIdentifierGenerator, error) {
	topLevelKey, _ := model_core.Patch(c.discardingObjectCapturer, key).SortAndSetReferences()
	anyKey, err := model_core.MarshalTopLevelAny(topLevelKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal key for creating an identifier generator for reference equal values: %w", err)
	}
	marshaledKey, err := model_core.MarshalTopLevelMessage(anyKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal key for creating an identifier generator for reference equal values: %w", err)
	}
	hashedKey := sha256.Sum256(marshaledKey)

	var nextIdentifier [16]byte
	copy(nextIdentifier[:], hashedKey[:])

	return func() []byte {
		identifier := append([]byte(nil), nextIdentifier[:]...)
		for i := 0; i < len(nextIdentifier); i++ {
			nextIdentifier[i]++
			if nextIdentifier[i] != 0 {
				break
			}
		}
		return identifier
	}, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeCompiledBzlFileValue(ctx context.Context, key *model_analysis_pb.CompiledBzlFile_Key, e CompiledBzlFileEnvironment[TReference, TMetadata]) (PatchedCompiledBzlFileValue[TMetadata], error) {
	canonicalLabel, err := label.NewCanonicalLabel(key.Label)
	if err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, fmt.Errorf("invalid label: %w", err)
	}
	canonicalPackage := canonicalLabel.GetCanonicalPackage()
	canonicalRepo := canonicalPackage.GetCanonicalRepo()

	thread := c.newStarlarkThread(ctx, e, key.BuiltinsModuleNames)

	bzlFileProperties := e.GetFilePropertiesValue(&model_analysis_pb.FileProperties_Key{
		CanonicalRepo: canonicalRepo.String(),
		Path:          canonicalLabel.GetRepoRelativePath(),
	})
	fileReader, gotFileReader := e.GetFileReaderValue(&model_analysis_pb.FileReader_Key{})
	bzlFileBuiltins, bzlFileBuiltinsErr := c.getBzlFileBuiltins(thread, e, key.BuiltinsModuleNames)
	if !bzlFileProperties.IsSet() || !gotFileReader {
		return PatchedCompiledBzlFileValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	if bzlFileBuiltinsErr != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, bzlFileBuiltinsErr
	}

	if bzlFileProperties.Message.Exists == nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, fmt.Errorf("file %#v does not exist", canonicalLabel.String())
	}
	buildFileContentsEntry, err := model_filesystem.NewFileContentsEntryFromProto(
		model_core.Nested(bzlFileProperties, bzlFileProperties.Message.Exists.GetContents()),
	)
	if err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, fmt.Errorf("invalid file contents: %w", err)
	}
	bzlFileData, err := fileReader.FileReadAll(ctx, buildFileContentsEntry, 1<<22)
	if err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, err
	}

	// TODO: @@builtins_core+//:exports.bzl needs to use recursion
	// in a couple of places. We should drop support for this once
	// we remove the C++ bits from this file.
	allowRecursion := false
	if canonicalRepo.String() == "builtins_core+" {
		allowRecursion = true
	}

	_, program, err := starlark.SourceProgramOptions(
		&syntax.FileOptions{
			Set:       true,
			Recursion: allowRecursion,
		},
		canonicalLabel.String(),
		bzlFileData,
		bzlFileBuiltins.Has,
	)
	if err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, err
	}

	if err := c.preloadBzlGlobals(e, canonicalPackage, program, key.BuiltinsModuleNames); err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, err
	}

	identifierGenerator, err := c.getReferenceEqualIdentifierGenerator(model_core.NewSimpleMessage[TReference](proto.Message(key)))
	if err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, err
	}
	thread.SetLocal(model_starlark.ReferenceEqualIdentifierGeneratorKey, identifierGenerator)

	globals, err := program.Init(thread, bzlFileBuiltins)
	if err != nil {
		if !errors.Is(err, evaluation.ErrMissingDependency) {
			var evalErr *starlark.EvalError
			if errors.As(err, &evalErr) {
				return PatchedCompiledBzlFileValue[TMetadata]{}, errors.New(evalErr.Backtrace())
			}
		}
		return PatchedCompiledBzlFileValue[TMetadata]{}, err
	}
	model_starlark.NameAndExtractGlobals(globals, canonicalLabel)

	// TODO! Use proper encoding options!
	compiledProgram, err := model_starlark.EncodeCompiledProgram(program, globals, c.getValueEncodingOptions(e, &canonicalLabel))
	if err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_analysis_pb.CompiledBzlFile_Value{
			CompiledProgram: compiledProgram.Message,
		},
		compiledProgram.Patcher,
	), nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeCompiledBzlFileDecodedGlobalsValue(ctx context.Context, key *model_analysis_pb.CompiledBzlFileDecodedGlobals_Key, e CompiledBzlFileDecodedGlobalsEnvironment[TReference, TMetadata]) (starlark.StringDict, error) {
	currentFilename, err := label.NewCanonicalLabel(key.Label)
	if err != nil {
		return nil, fmt.Errorf("invalid label: %w", err)
	}
	compiledBzlFile := e.GetCompiledBzlFileValue(&model_analysis_pb.CompiledBzlFile_Key{
		Label:               currentFilename.String(),
		BuiltinsModuleNames: key.BuiltinsModuleNames,
	})
	if !compiledBzlFile.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	return model_starlark.DecodeGlobals[TReference, TMetadata](
		model_core.Nested(compiledBzlFile, compiledBzlFile.Message.CompiledProgram.GetGlobals()),
		currentFilename,
		c.getValueDecodingOptions(ctx, func(resolvedLabel label.ResolvedLabel) (starlark.Value, error) {
			return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
		}),
	)
}

func (c *baseComputer[TReference, TMetadata]) ComputeCompiledBzlFileFunctionFactoryValue(ctx context.Context, key *model_analysis_pb.CompiledBzlFileFunctionFactory_Key, e CompiledBzlFileFunctionFactoryEnvironment[TReference, TMetadata]) (*starlark.FunctionFactory, error) {
	canonicalLabel, err := label.NewCanonicalLabel(key.Label)
	if err != nil {
		return nil, err
	}
	thread := c.newStarlarkThread(ctx, e, key.BuiltinsModuleNames)

	compiledBzlFile := e.GetCompiledBzlFileValue(&model_analysis_pb.CompiledBzlFile_Key{
		Label:               canonicalLabel.String(),
		BuiltinsModuleNames: key.BuiltinsModuleNames,
	})
	bzlFileBuiltins, bzlFileBuiltinsErr := c.getBzlFileBuiltins(thread, e, key.BuiltinsModuleNames)
	if !compiledBzlFile.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	if bzlFileBuiltinsErr != nil {
		return nil, bzlFileBuiltinsErr
	}

	identifierGenerator, err := c.getReferenceEqualIdentifierGenerator(model_core.NewSimpleMessage[TReference](proto.Message(key)))
	if err != nil {
		return nil, err
	}
	thread.SetLocal(model_starlark.ReferenceEqualIdentifierGeneratorKey, identifierGenerator)

	program, err := starlark.CompiledProgram(bytes.NewBuffer(compiledBzlFile.Message.CompiledProgram.GetCode()))
	if err != nil {
		return nil, fmt.Errorf("failed to load previously compiled file %#v: %w", key.Label, err)
	}
	if err := c.preloadBzlGlobals(e, canonicalLabel.GetCanonicalPackage(), program, key.BuiltinsModuleNames); err != nil {
		return nil, err
	}

	functionFactory, globals, err := program.NewFunctionFactory(thread, bzlFileBuiltins)
	if err != nil {
		return nil, err
	}
	model_starlark.NameAndExtractGlobals(globals, canonicalLabel)
	globals.Freeze()
	return functionFactory, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeCompiledBzlFileGlobalValue(ctx context.Context, key *model_analysis_pb.CompiledBzlFileGlobal_Key, e CompiledBzlFileGlobalEnvironment[TReference, TMetadata]) (PatchedCompiledBzlFileGlobalValue[TMetadata], error) {
	identifier, err := label.NewCanonicalStarlarkIdentifier(key.Identifier)
	if err != nil {
		return PatchedCompiledBzlFileGlobalValue[TMetadata]{}, fmt.Errorf("invalid identifier: %w", err)
	}

	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	if !allBuiltinsModulesNames.IsSet() {
		return PatchedCompiledBzlFileGlobalValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	compiledBzlFile := e.GetCompiledBzlFileValue(&model_analysis_pb.CompiledBzlFile_Key{
		Label:               identifier.GetCanonicalLabel().String(),
		BuiltinsModuleNames: allBuiltinsModulesNames.Message.BuiltinsModuleNames,
	})
	if !compiledBzlFile.IsSet() {
		return PatchedCompiledBzlFileGlobalValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	global, err := model_starlark.GetStructFieldValue(
		ctx,
		c.valueReaders.List,
		model_core.Nested(compiledBzlFile, compiledBzlFile.Message.CompiledProgram.GetGlobals()),
		identifier.GetStarlarkIdentifier().String(),
	)
	if err != nil {
		return PatchedCompiledBzlFileGlobalValue[TMetadata]{}, err
	}

	patchedGlobal := model_core.Patch(e, global)
	return model_core.NewPatchedMessage(
		&model_analysis_pb.CompiledBzlFileGlobal_Value{
			Global: patchedGlobal.Message,
		},
		patchedGlobal.Patcher,
	), nil
}

var exportsBzlTargetName = util.Must(label.NewTargetName("exports.bzl"))

type getBzlFileBuiltinsEnvironment[TReference any] interface {
	GetCompiledBzlFileDecodedGlobalsValue(key *model_analysis_pb.CompiledBzlFileDecodedGlobals_Key) (starlark.StringDict, bool)
}

func (c *baseComputer[TReference, TMetadata]) getBzlFileBuiltins(thread *starlark.Thread, e getBzlFileBuiltinsEnvironment[TReference], builtinsModuleNames []string) (starlark.StringDict, error) {
	allToplevels := starlark.StringDict{}
	for name, value := range c.bzlFileBuiltins {
		allToplevels[name] = value
	}

	newNative := map[string]any{}
	gotAllGlobals := true
	for i, builtinsModuleNameStr := range builtinsModuleNames {
		builtinsModuleName, err := label.NewModule(builtinsModuleNameStr)
		if err != nil {
			return nil, fmt.Errorf("invalid module name %#v: %w", builtinsModuleNameStr, err)
		}
		exportsFile := builtinsModuleName.
			ToModuleInstance(nil).
			GetBareCanonicalRepo().
			GetRootPackage().
			AppendTargetName(exportsBzlTargetName).
			String()
		globals, gotGlobals := e.GetCompiledBzlFileDecodedGlobalsValue(&model_analysis_pb.CompiledBzlFileDecodedGlobals_Key{
			Label:               exportsFile,
			BuiltinsModuleNames: builtinsModuleNames[:i],
		})
		gotAllGlobals = gotAllGlobals && gotGlobals
		if gotAllGlobals {
			exportedToplevels, ok := globals["exported_toplevels"].(starlark.IterableMapping)
			if !ok {
				return nil, fmt.Errorf("file %#v does not declare \"exported_toplevels\"", exportsFile)
			}
			for name, value := range starlark.Entries(thread, exportedToplevels) {
				nameStr, ok := starlark.AsString(name)
				if !ok {
					return nil, fmt.Errorf("file %#v exports builtins with non-string names", exportsFile)
				}
				allToplevels[strings.TrimPrefix(nameStr, "+")] = value
			}

			exportedRules, ok := globals["exported_rules"].(starlark.IterableMapping)
			if !ok {
				return nil, fmt.Errorf("file %#v does not declare \"exported_rules\"", exportsFile)
			}
			for name, value := range starlark.Entries(thread, exportedRules) {
				nameStr, ok := starlark.AsString(name)
				if !ok {
					return nil, fmt.Errorf("file %#v exports builtins with non-string names", exportsFile)
				}
				newNative[strings.TrimPrefix(nameStr, "+")] = value
			}
		}
	}
	if !gotAllGlobals {
		return nil, evaluation.ErrMissingDependency
	}

	// Expose all rules via native.${name}().
	existingNative, ok := allToplevels["native"].(*model_starlark.Struct[TReference, TMetadata])
	if !ok {
		return nil, errors.New("exported builtins do not declare \"native\"")
	}
	for name, value := range existingNative.ToDict() {
		if _, ok := newNative[name]; !ok {
			newNative[name] = value
		}
	}
	allToplevels["native"] = model_starlark.NewStructFromDict[TReference, TMetadata](nil, newNative)

	return allToplevels, nil
}

func (c *baseComputer[TReference, TMetadata]) getBuildFileBuiltins(thread *starlark.Thread, e getBzlFileBuiltinsEnvironment[TReference], builtinsModuleNames []string) (starlark.StringDict, error) {
	allRules := starlark.StringDict{}
	for name, value := range c.buildFileBuiltins {
		allRules[name] = value
	}

	gotAllGlobals := true
	for i, builtinsModuleNameStr := range builtinsModuleNames {
		builtinsModuleName, err := label.NewModule(builtinsModuleNameStr)
		if err != nil {
			return nil, fmt.Errorf("invalid module name %#v: %w", builtinsModuleNameStr, err)
		}
		exportsFile := builtinsModuleName.
			ToModuleInstance(nil).
			GetBareCanonicalRepo().
			GetRootPackage().
			AppendTargetName(exportsBzlTargetName).
			String()
		globals, gotGlobals := e.GetCompiledBzlFileDecodedGlobalsValue(&model_analysis_pb.CompiledBzlFileDecodedGlobals_Key{
			Label:               exportsFile,
			BuiltinsModuleNames: builtinsModuleNames[:i],
		})
		gotAllGlobals = gotAllGlobals && gotGlobals
		if gotAllGlobals {
			exportedRules, ok := globals["exported_rules"].(starlark.IterableMapping)
			if !ok {
				return nil, fmt.Errorf("file %#v does not declare \"exported_rules\"", exportsFile)
			}
			for name, value := range starlark.Entries(thread, exportedRules) {
				nameStr, ok := starlark.AsString(name)
				if !ok {
					return nil, fmt.Errorf("file %#v exports builtins with non-string names", exportsFile)
				}
				allRules[strings.TrimPrefix(nameStr, "+")] = value
			}
		}
	}
	if !gotAllGlobals {
		return nil, evaluation.ErrMissingDependency
	}

	return allRules, nil
}
