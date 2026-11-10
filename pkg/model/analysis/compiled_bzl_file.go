package analysis

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/proto"
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
	keyReference, err := model_core.ComputeTopLevelMessageReference(anyKey, c.referenceFormat)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal key for creating an identifier generator for reference equal values: %w", err)
	}

	var nextIdentifier [16]byte
	copy(nextIdentifier[:], keyReference.GetRawReference())

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
	valueEncodingOptionsFactory, err := c.newValueEncodingOptionsFactory(e)
	if err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, err
	}

	canonicalLabel, err := label.NewCanonicalLabel(key.Label)
	if err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, fmt.Errorf("invalid label: %w", err)
	}
	canonicalPackage := canonicalLabel.GetCanonicalPackage()
	canonicalRepo := canonicalPackage.GetCanonicalRepo()

	thread, err := c.newStarlarkThread(ctx, e, key.BuiltinsModuleNames)
	if err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, err
	}

	bzlFileProperties := e.GetFilePropertiesValue(&model_analysis_pb.FileProperties_Key{
		CanonicalRepo: canonicalRepo.String(),
		Path:          canonicalLabel.GetRepoRelativePath(),
	})
	fileReader, gotFileReader := e.GetFileAccessParametersObject(&model_analysis_pb.FileAccessParameters_Key{})
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

	program, err := c.loadProgram(e, canonicalLabel, bzlFileData, &resolve.BzlDialect, bzlFileBuiltins, key.BuiltinsModuleNames)
	if err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, err
	}

	identifierGenerator, err := c.getReferenceEqualIdentifierGenerator(model_core.NewSimpleMessage[TReference](proto.Message(key)))
	if err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, err
	}
	thread.SetLocal(model_starlark.ReferenceEqualIdentifierGeneratorKey, identifierGenerator)

	module := model_starlark.NewBzlModule(canonicalLabel, bzlFileBuiltins, c.newBzlImporter(ctx, e, key.BuiltinsModuleNames))
	if err := eval.ExecFile(thread, program, module); err != nil {
		return PatchedCompiledBzlFileValue[TMetadata]{}, formatStarlarkError(err)
	}
	globals := module.Globals()

	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.CompiledBzlFile_Value, error) {
		compiledProgram, err := model_starlark.EncodeCompiledProgram(thread, program, globals, valueEncodingOptionsFactory.newValueEncodingOptions(ctx, &canonicalLabel))
		if err != nil {
			return nil, err
		}
		return &model_analysis_pb.CompiledBzlFile_Value{
			CompiledProgram: compiledProgram.Merge(patcher),
		}, nil
	})
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

// forEachBuiltinsModuleExport calls a function for each entry of a dict
// that is exported by the exports.bzl file of each builtins module. The
// values of the dicts are decoded on the provided thread.
func (c *baseComputer[TReference, TMetadata]) forEachBuiltinsModuleExport(thread *eval.Thread, e CompiledBzlFileValueGetter[TReference], builtinsModuleNames []string, globalName string, yield func(name string, value eval.Value)) error {
	missingDependencies := false
	for i, builtinsModuleNameStr := range builtinsModuleNames {
		builtinsModuleName, err := label.NewModule(builtinsModuleNameStr)
		if err != nil {
			return fmt.Errorf("invalid module name %#v: %w", builtinsModuleNameStr, err)
		}
		exportsFile := builtinsModuleName.
			ToModuleInstance(nil).
			GetBareCanonicalRepo().
			GetRootPackage().
			AppendTargetName(exportsBzlTargetName)
		identifier := exportsFile.AppendStarlarkIdentifier(util.Must(label.NewStarlarkIdentifier(globalName)))
		encodedValue, err := c.getBzlGlobal(thread.Context(), e, identifier, builtinsModuleNames[:i])
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
			return err
		}
		if missingDependencies {
			continue
		}
		value, err := model_starlark.DecodeValue[TReference, TMetadata](thread, encodedValue, &identifier)
		if err != nil {
			return err
		}
		exports, ok := value.(eval.Dict)
		if !ok {
			return fmt.Errorf("%#v is not a dict", identifier.String())
		}
		var errIter error
		for name, value := range exports.Items(thread, &errIter) {
			nameStr, ok, err := eval.AsString(thread, name)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%#v contains builtins with non-string names", identifier.String())
			}
			yield(strings.TrimPrefix(nameStr, "+"), value)
		}
		if errIter != nil {
			return errIter
		}
	}
	if missingDependencies {
		return evaluation.ErrMissingDependency
	}
	return nil
}

// getBzlFileBuiltins returns the names that are predeclared by .bzl
// files. In addition to the builtins provided by Bonanza, these include
// the ones exported by builtins modules.
func (c *baseComputer[TReference, TMetadata]) getBzlFileBuiltins(thread *eval.Thread, e CompiledBzlFileValueGetter[TReference], builtinsModuleNames []string) (map[string]eval.Value, error) {
	allToplevels := maps.Clone(c.bzlFileBuiltins)
	if err := c.forEachBuiltinsModuleExport(thread, e, builtinsModuleNames, "exported_toplevels", func(name string, value eval.Value) {
		allToplevels[name] = value
	}); err != nil {
		return nil, err
	}
	newNative := map[string]any{}
	if err := c.forEachBuiltinsModuleExport(thread, e, builtinsModuleNames, "exported_rules", func(name string, value eval.Value) {
		newNative[name] = value
	}); err != nil {
		return nil, err
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

// getBuildFileBuiltins returns the names that are predeclared by BUILD
// files. In addition to the builtins provided by Bonanza, these include
// the rules exported by builtins modules.
func (c *baseComputer[TReference, TMetadata]) getBuildFileBuiltins(thread *eval.Thread, e CompiledBzlFileValueGetter[TReference], builtinsModuleNames []string) (map[string]eval.Value, error) {
	allRules := maps.Clone(c.buildFileBuiltins)
	if err := c.forEachBuiltinsModuleExport(thread, e, builtinsModuleNames, "exported_rules", func(name string, value eval.Value) {
		allRules[name] = value
	}); err != nil {
		return nil, err
	}
	return allRules, nil
}
