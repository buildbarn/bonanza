package analysis

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

func (c *baseComputer[TReference, TMetadata]) decodeStringDict(ctx context.Context, fileReader *model_filesystem.FileReader[TReference], d model_core.Message[*model_starlark_pb.Value, TReference]) (map[string]string, error) {
	dict, ok := d.Message.GetKind().(*model_starlark_pb.Value_Dict)
	if !ok {
		return nil, errors.New("not a dict")
	}
	var iterErr error
	o := map[string]string{}
	for key, value := range model_starlark.AllDictLeafEntries(
		ctx,
		c.valueReaders.Dict,
		model_core.Nested(d, dict.Dict),
		&iterErr,
	) {
		keyStr, ok, err := getStringValue(ctx, fileReader, key)
		if err != nil {
			return nil, fmt.Errorf("key: %w", err)
		}
		if !ok {
			return nil, errors.New("key is not a string")
		}
		valueStr, ok, err := getStringValue(ctx, fileReader, value)
		if err != nil {
			return nil, fmt.Errorf("value of key %#v: %w", keyStr, err)
		}
		if !ok {
			return nil, errors.New("value is not a string")
		}
		o[keyStr] = valueStr
	}
	if iterErr != nil {
		return nil, fmt.Errorf("failed to iterate dict: %w", iterErr)
	}
	return o, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeRegisteredRepoPlatformValue(ctx context.Context, key *model_analysis_pb.RegisteredRepoPlatform_Key, e RegisteredRepoPlatformEnvironment[TReference, TMetadata]) (PatchedRegisteredRepoPlatformValue[TMetadata], error) {
	// Obtain the label of the repo platform that was provided by
	// the client through the --repo_platform command line flag.
	buildSpecificationValue := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecificationValue.IsSet() {
		return PatchedRegisteredRepoPlatformValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	buildSpecification := buildSpecificationValue.Message
	fileReader, err := getFileReader(e)
	if err != nil {
		return PatchedRegisteredRepoPlatformValue[TMetadata]{}, err
	}

	rootModuleName := buildSpecification.RootModuleName
	rootModule, err := label.NewModule(rootModuleName)
	if err != nil {
		return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("invalid root module name %#v: %w", rootModuleName, err)
	}
	rootRepo := rootModule.ToModuleInstance(nil).GetBareCanonicalRepo()

	repoPlatformStr := buildSpecification.RepoPlatform
	if repoPlatformStr == "" {
		return PatchedRegisteredRepoPlatformValue[TMetadata]{}, errors.New("no repo platform specified, meaning module extensions and repository rules cannot be evaluated")
	}
	apparentRepoPlatformLabel, err := rootRepo.GetRootPackage().AppendLabel(repoPlatformStr)
	if err != nil {
		return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("invalid repo platform label %#v: %w", repoPlatformStr, err)
	}
	repoPlatformLabel, err := label.Canonicalize(newLabelResolver(e), rootRepo, apparentRepoPlatformLabel)
	if err != nil {
		return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("failed to resolve repo platform label %#v: %w", apparentRepoPlatformLabel.String(), err)
	}
	repoPlatformLabelStr := repoPlatformLabel.String()

	// Obtain the PlatformInfo provider of the repo platform.
	platformInfoProvider, err := getProviderFromConfiguredTarget(
		e,
		repoPlatformLabelStr,
		model_core.NewSimplePatchedMessage[TMetadata]((*model_core_pb.DecodableReference)(nil)),
		platformInfoProviderIdentifier,
	)
	if err != nil {
		return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("failed to obtain PlatformInfo of repo platform %#v: %w", repoPlatformLabelStr, err)
	}

	// Extract fields from the PlatformInfo provider that are needed
	// when evaluating module extensions and repository rules. We
	// don't need to extract the constraints, as those are only used
	// by the toolchain resolution process of regular build rules.
	var execPKIXPublicKey []byte
	var repositoryOSArch, repositoryOSName string
	var repositoryOSEnviron []*model_analysis_pb.RegisteredRepoPlatform_Value_EnvironmentVariable
	var errIter error
	for key, value := range model_starlark.AllStructFields(
		ctx,
		c.valueReaders.List,
		platformInfoProvider,
		&errIter,
	) {
		switch key {
		case "exec_pkix_public_key":
			str, ok, err := getStringValue(ctx, fileReader, value)
			if err != nil {
				return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("exec_pkix_public_key field of PlatformInfo of repo platform %#v: %w", repoPlatformLabelStr, err)
			}
			if !ok {
				return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("exec_pkix_public_key field of PlatformInfo of repo platform %#v is not a string", repoPlatformLabelStr)
			}
			execPKIXPublicKey, err = base64.StdEncoding.DecodeString(str)
			if err != nil {
				return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("exec_pkix_public_key field of PlatformInfo of repo platform %#v: %w", repoPlatformLabelStr, err)
			}
		case "repository_os_arch":
			str, ok, err := getStringValue(ctx, fileReader, value)
			if err != nil {
				return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("repository_os_arch field of PlatformInfo of repo platform %#v: %w", repoPlatformLabelStr, err)
			}
			if !ok {
				return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("repository_os_arch field of PlatformInfo of repo platform %#v is not a string", repoPlatformLabelStr)
			}
			repositoryOSArch = str
		case "repository_os_environ":
			p, err := c.decodeStringDict(ctx, fileReader, value)
			if err != nil {
				return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("repository_os_environ field of PlatformInfo of repo platform %#v: %w", repoPlatformLabelStr, err)
			}
			repositoryOSEnviron = make([]*model_analysis_pb.RegisteredRepoPlatform_Value_EnvironmentVariable, 0, len(p))
			for _, name := range slices.Sorted(maps.Keys(p)) {
				repositoryOSEnviron = append(repositoryOSEnviron, &model_analysis_pb.RegisteredRepoPlatform_Value_EnvironmentVariable{
					Name:  name,
					Value: p[name],
				})
			}
		case "repository_os_name":
			str, ok, err := getStringValue(ctx, fileReader, value)
			if err != nil {
				return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("repository_os_name field of PlatformInfo of repo platform %#v: %w", repoPlatformLabelStr, err)
			}
			if !ok {
				return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("repository_os_name field of PlatformInfo of repo platform %#v is not a string", repoPlatformLabelStr)
			}
			repositoryOSName = str
		}
	}
	if errIter != nil {
		return PatchedRegisteredRepoPlatformValue[TMetadata]{}, errIter
	}

	if len(execPKIXPublicKey) == 0 {
		return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("exec_pkix_public_key field of PlatformInfo of repo platform %#v is not set to a non-empty string", repoPlatformLabelStr)
	}
	if repositoryOSArch == "" {
		return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("repository_os_arch field of PlatformInfo of repo platform %#v is not set to a non-empty string", repoPlatformLabelStr)
	}
	if repositoryOSEnviron == nil {
		return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("PlatformInfo of repo platform %#v does not contain field repository_os_environ", repoPlatformLabelStr)
	}
	if repositoryOSName == "" {
		return PatchedRegisteredRepoPlatformValue[TMetadata]{}, fmt.Errorf("repository_os_name field of PlatformInfo of repo platform %#v is not set to a non-empty string", repoPlatformLabelStr)
	}

	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_analysis_pb.RegisteredRepoPlatform_Value{
			ExecPkixPublicKey:   execPKIXPublicKey,
			RepositoryOsArch:    repositoryOSArch,
			RepositoryOsEnviron: repositoryOSEnviron,
			RepositoryOsName:    repositoryOSName,
		},
	), nil
}
