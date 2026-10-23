package arguments_test

import (
	"bytes"
	"syscall"
	"testing"

	"bonanza.build/pkg/bazelclient/arguments"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestParse(t *testing.T) {
	ctrl := gomock.NewController(t)

	t.Run("Build", func(t *testing.T) {
		rootDirectory := NewMockDirectory(ctrl)

		command, err := arguments.Parse(
			[]string{
				"--ignore_all_rc_files",
				"build",
				"--keep_going",
				"//...",
			},
			rootDirectory,
			path.UNIXFormat,
			/* workspacePath = */ path.UNIXFormat.NewParser("/home/bob/myproject"),
			/* homeDirectoryPath = */ path.UNIXFormat.NewParser("/home/bob"),
			/* workingDirectoryPath = */ path.UNIXFormat.NewParser("/home/bob/myproject/src"),
		)
		require.NoError(t, err)
		require.Equal(t, &arguments.BuildCommand{
			CommonFlags: arguments.CommonFlags{
				Color:                  arguments.Color_Auto,
				LockfileMode:           arguments.LockfileMode_Update,
				MessageFormat:          arguments.MessageFormat_Pretty,
				RemoteCacheCompression: true,
			},
			BuildFlags: arguments.BuildFlags{
				KeepGoing: true,
			},
			Arguments: []string{"//..."},
		}, command)
	})

	t.Run("Version", func(t *testing.T) {
		// Perform an end to end test, where we have a
		// simple .bazelrc file in the home directory
		// that contains "version --gnu_format". When we
		// run "bazel version", this should cause it to
		// print just the program name and version
		// number.
		rootDirectory := NewMockDirectory(ctrl)

		etcDirectory := NewMockDirectoryCloser(ctrl)
		rootDirectory.EXPECT().EnterDirectory(path.MustNewComponent("etc")).
			Return(etcDirectory, nil)
		etcDirectory.EXPECT().OpenRead(path.MustNewComponent("bazel.bazelrc")).
			Return(nil, syscall.ENOENT)
		etcDirectory.EXPECT().
			Close()

		homeDirectory := NewMockDirectoryCloser(ctrl)
		rootDirectory.EXPECT().EnterDirectory(path.MustNewComponent("home")).
			Return(homeDirectory, nil).
			Times(3)
		homeDirectory.EXPECT().
			Close().
			Times(3)

		bobDirectory := NewMockDirectoryCloser(ctrl)
		homeDirectory.EXPECT().EnterDirectory(path.MustNewComponent("bob")).
			Return(bobDirectory, nil).
			Times(3)
		bazelRCFile := NewMockFileReader(ctrl)
		bazelRCFile.EXPECT().ReadAt(gomock.Any(), gomock.Any()).
			DoAndReturn(bytes.NewReader([]byte(`version --gnu_format`)).ReadAt).
			MinTimes(1)
		bazelRCFile.EXPECT().Close()
		bobDirectory.EXPECT().OpenRead(path.MustNewComponent(".bazelrc")).
			Return(bazelRCFile, nil)
		bobDirectory.EXPECT().
			Close().
			Times(3)

		myProjectDirectory := NewMockDirectoryCloser(ctrl)
		bobDirectory.EXPECT().EnterDirectory(path.MustNewComponent("myproject")).
			Return(myProjectDirectory, nil).
			Times(2)
		myProjectDirectory.EXPECT().OpenRead(path.MustNewComponent(".bazelrc")).
			Return(nil, syscall.ENOENT)
		myProjectDirectory.EXPECT().
			Close().
			Times(2)

		srcDirectory := NewMockDirectoryCloser(ctrl)
		myProjectDirectory.EXPECT().EnterDirectory(path.MustNewComponent("src")).
			Return(srcDirectory, nil)
		srcDirectory.EXPECT().
			Close()

		command, err := arguments.Parse(
			[]string{
				"version",
			},
			rootDirectory,
			path.UNIXFormat,
			/* workspacePath = */ path.UNIXFormat.NewParser("/home/bob/myproject"),
			/* homeDirectoryPath = */ path.UNIXFormat.NewParser("/home/bob"),
			/* workingDirectoryPath = */ path.UNIXFormat.NewParser("/home/bob/myproject/src"),
		)
		require.NoError(t, err)
		require.Equal(t, arguments.VersionFlags{
			GnuFormat: true,
		}, command.(*arguments.VersionCommand).VersionFlags)
	})
}
