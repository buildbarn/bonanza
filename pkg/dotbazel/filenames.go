package dotbazel

import (
	"bonanza.build/pkg/label"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"
)

const (
	// BuildFilename is the name of the file that declares the
	// targets contained in a package.
	BuildFilename = "BUILD.bazel"

	// LegacyBuildFilename is the name of the file that declares
	// the targets contained in a package, which is used if
	// BuildFilename does not exist.
	LegacyBuildFilename = "BUILD"

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
	// BuildComponent is the pathname component of BuildFilename.
	BuildComponent = path.MustNewComponent(BuildFilename)

	// LegacyBuildComponent is the pathname component of
	// LegacyBuildFilename.
	LegacyBuildComponent = path.MustNewComponent(LegacyBuildFilename)

	// BuildComponents contains the pathname components of all
	// files that may declare the targets contained in a package,
	// in order of preference.
	BuildComponents = []path.Component{
		BuildComponent,
		LegacyBuildComponent,
	}

	// BuildTargetName is the target name of BuildFilename within a
	// package.
	BuildTargetName = util.Must(label.NewTargetName(BuildFilename))

	// LegacyBuildTargetName is the target name of
	// LegacyBuildFilename within a package.
	LegacyBuildTargetName = util.Must(label.NewTargetName(LegacyBuildFilename))

	// BuildTargetNames contains the target names of all files that
	// may declare the targets contained in a package, in order of
	// preference.
	BuildTargetNames = []label.TargetName{
		BuildTargetName,
		LegacyBuildTargetName,
	}

	// ModuleComponent is the pathname component of ModuleFilename.
	ModuleComponent = path.MustNewComponent(ModuleFilename)

	// RepoComponent is the pathname component of RepoFilename.
	RepoComponent = path.MustNewComponent(RepoFilename)

	// ModuleTargetName is the target name of ModuleFilename within
	// the root package of a repository.
	ModuleTargetName = util.Must(label.NewTargetName(ModuleFilename))

	// RepoTargetName is the target name of RepoFilename within the
	// root package of a repository.
	RepoTargetName = util.Must(label.NewTargetName(RepoFilename))
)
