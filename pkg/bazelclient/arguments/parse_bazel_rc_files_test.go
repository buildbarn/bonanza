package arguments_test

import (
	"bytes"
	"syscall"
	"testing"

	"bonanza.build/pkg/bazelclient/arguments"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

func TestParseBazelRCFiles(t *testing.T) {
	ctrl := gomock.NewController(t)

	t.Run("None", func(t *testing.T) {
		rootDirectory := NewMockDirectory(ctrl)
		directives, err := arguments.ParseBazelRCFiles(
			/* bazelRCPaths = */ nil,
			rootDirectory,
			path.UNIXFormat,
			/* workspacePath = */ nil,
			/* workingDirectoryPath = */ path.UNIXFormat.NewParser("/"),
		)
		require.NoError(t, err)
		require.Empty(t, directives)
	})

	t.Run("NonExistentPath", func(t *testing.T) {
		rootDirectory := NewMockDirectory(ctrl)
		rootDirectory.EXPECT().OpenRead(path.MustNewComponent("nonexistent")).
			Return(nil, syscall.ENOENT)

		_, err := arguments.ParseBazelRCFiles(
			[]arguments.BazelRCPath{{
				Path:     path.UNIXFormat.NewParser("/nonexistent"),
				Required: true,
			}},
			rootDirectory,
			path.UNIXFormat,
			/* workspacePath = */ path.UNIXFormat.NewParser("/home/bob/myproject"),
			/* workingDirectoryPath = */ path.UNIXFormat.NewParser("/"),
		)
		// TODO: Polish up error message!
		testutil.RequirePrefixedStatus(t, status.Error(codes.NotFound, "Directory \"/\": "), err)
	})

	t.Run("SymlinkLoopTerminal", func(t *testing.T) {
		rootDirectory := NewMockDirectory(ctrl)
		rootDirectory.EXPECT().OpenRead(path.MustNewComponent("symlink")).
			Return(nil, syscall.ELOOP).
			MinTimes(1)
		rootDirectory.EXPECT().Readlink(path.MustNewComponent("symlink")).
			Return(path.UNIXFormat.NewParser("/symlink"), nil).
			MinTimes(1)

		_, err := arguments.ParseBazelRCFiles(
			[]arguments.BazelRCPath{{
				Path:     path.UNIXFormat.NewParser("/symlink"),
				Required: true,
			}},
			rootDirectory,
			path.UNIXFormat,
			/* workspacePath = */ path.UNIXFormat.NewParser("/home/bob/myproject"),
			/* workingDirectoryPath = */ path.UNIXFormat.NewParser("/"),
		)
		// TODO: Polish up error message!
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Directory \"/\": Maximum number of symbolic link redirections reached"), err)
	})

	t.Run("SymlinkLoopDirectory", func(t *testing.T) {
		rootDirectory := NewMockDirectory(ctrl)
		rootDirectory.EXPECT().EnterDirectory(path.MustNewComponent("symlink")).
			Return(nil, syscall.ENOTDIR).
			MinTimes(1)
		rootDirectory.EXPECT().Readlink(path.MustNewComponent("symlink")).
			Return(path.UNIXFormat.NewParser("/symlink/symlink"), nil).
			MinTimes(1)

		_, err := arguments.ParseBazelRCFiles(
			[]arguments.BazelRCPath{{
				Path:     path.UNIXFormat.NewParser("/symlink/myconfig"),
				Required: true,
			}},
			rootDirectory,
			path.UNIXFormat,
			/* workspacePath = */ path.UNIXFormat.NewParser("/home/bob/myproject"),
			/* workingDirectoryPath = */ path.UNIXFormat.NewParser("/"),
		)
		// TODO: Polish up error message!
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Directory \"/\": Maximum number of symbolic link redirections reached"), err)
	})

	t.Run("PathIsDirectory", func(t *testing.T) {
		rootDirectory := NewMockDirectory(ctrl)
		childDirectory := NewMockDirectoryCloser(ctrl)
		rootDirectory.EXPECT().EnterDirectory(path.MustNewComponent("directory")).
			Return(childDirectory, nil)
		childDirectory.EXPECT().Close()

		_, err := arguments.ParseBazelRCFiles(
			[]arguments.BazelRCPath{{
				Path:     path.UNIXFormat.NewParser("/directory/"),
				Required: true,
			}},
			rootDirectory,
			path.UNIXFormat,
			/* workspacePath = */ path.UNIXFormat.NewParser("/home/bob/myproject"),
			/* workingDirectoryPath = */ path.UNIXFormat.NewParser("/"),
		)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Path \"/directory/\" resolves to a directory"), err)
	})

	t.Run("SimpleSuccess", func(t *testing.T) {
		rootDirectory := NewMockDirectory(ctrl)
		r := NewMockFileReader(ctrl)
		rootDirectory.EXPECT().OpenRead(path.MustNewComponent("hello.txt")).
			Return(r, nil)
		r.EXPECT().ReadAt(gomock.Any(), gomock.Any()).
			DoAndReturn(
				bytes.NewReader([]byte(`
					build --jobs 300
					build --action_env=PATH=/bin:/sbin:/usr/bin:/usr/sbin
					test --flaky_test_attempts 3
				`)).ReadAt,
			).
			MinTimes(1)
		r.EXPECT().Close()

		directives, err := arguments.ParseBazelRCFiles(
			[]arguments.BazelRCPath{{
				Path:     path.UNIXFormat.NewParser("/hello.txt"),
				Required: true,
			}},
			rootDirectory,
			path.UNIXFormat,
			/* workspacePath = */ path.UNIXFormat.NewParser("/home/bob/myproject"),
			/* workingDirectoryPath = */ path.UNIXFormat.NewParser("/"),
		)
		require.NoError(t, err)
		require.Equal(t, arguments.ConfigurationDirectives{
			"build": {
				{"--jobs", "300"},
				{"--action_env=PATH=/bin:/sbin:/usr/bin:/usr/sbin"},
			},
			"test": {
				{"--flaky_test_attempts", "3"},
			},
		}, directives)
	})

	t.Run("MalformedImport", func(t *testing.T) {
		rootDirectory := NewMockDirectory(ctrl)
		r := NewMockFileReader(ctrl)
		rootDirectory.EXPECT().OpenRead(path.MustNewComponent("hello.txt")).
			Return(r, nil)
		r.EXPECT().ReadAt(gomock.Any(), gomock.Any()).
			DoAndReturn(
				bytes.NewReader([]byte(`
					import a b
				`)).ReadAt,
			)
		r.EXPECT().Close()

		_, err := arguments.ParseBazelRCFiles(
			[]arguments.BazelRCPath{{
				Path:     path.UNIXFormat.NewParser("/hello.txt"),
				Required: true,
			}},
			rootDirectory,
			path.UNIXFormat,
			/* workspacePath = */ path.UNIXFormat.NewParser("/home/bob/myproject"),
			/* workingDirectoryPath = */ path.UNIXFormat.NewParser("/"),
		)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "File \"/hello.txt\" contains an invalid import statement"), err)
	})

	t.Run("SelfImport", func(t *testing.T) {
		rootDirectory := NewMockDirectory(ctrl)
		r1 := NewMockFileReader(ctrl)
		rootDirectory.EXPECT().OpenRead(path.MustNewComponent("hello.txt")).
			Return(r1, nil)
		r1.EXPECT().ReadAt(gomock.Any(), gomock.Any()).
			DoAndReturn(
				bytes.NewReader([]byte(`
					import /goodbye.txt
				`)).ReadAt,
			)
		r1.EXPECT().Close()

		r2 := NewMockFileReader(ctrl)
		rootDirectory.EXPECT().OpenRead(path.MustNewComponent("goodbye.txt")).
			Return(r2, nil)
		r2.EXPECT().ReadAt(gomock.Any(), gomock.Any()).
			DoAndReturn(
				bytes.NewReader([]byte(`
					import /hello.txt
				`)).ReadAt,
			)
		r2.EXPECT().Close()

		r3 := NewMockFileReader(ctrl)
		rootDirectory.EXPECT().OpenRead(path.MustNewComponent("hello.txt")).
			Return(r3, nil)
		r3.EXPECT().Close()

		_, err := arguments.ParseBazelRCFiles(
			[]arguments.BazelRCPath{{
				Path:     path.UNIXFormat.NewParser("/hello.txt"),
				Required: true,
			}},
			rootDirectory,
			path.UNIXFormat,
			/* workspacePath = */ path.UNIXFormat.NewParser("/home/bob/myproject"),
			/* workingDirectoryPath = */ path.UNIXFormat.NewParser("/"),
		)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "In file \"/hello.txt\": In file \"/goodbye.txt\": Cyclic import of \"/hello.txt\""), err)
	})
}
