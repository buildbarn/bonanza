package dotbazel

import (
	"bonanza.build/pkg/label"

	"github.com/buildbarn/bb-storage/pkg/util"
)

const (
	// ModuleFilename is the name of the file at the root of a
	// module that declares the module's name, version and
	// dependencies. It is parsed using ParseModule().
	ModuleFilename = "MODULE.bazel"

	// RepoFilename is the name of the file at the root of a
	// repository that declares repository-wide settings, such as
	// default attributes of targets and directories to ignore. It
	// is parsed using ParseRepo().
	RepoFilename = "REPO.bazel"
)

var (
	// ModuleTargetName is the target name of ModuleFilename within
	// the root package of a repository.
	ModuleTargetName = util.Must(label.NewTargetName(ModuleFilename))

	// RepoTargetName is the target name of RepoFilename within the
	// root package of a repository.
	RepoTargetName = util.Must(label.NewTargetName(RepoFilename))
)
