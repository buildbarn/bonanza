package arguments_test

import (
	"testing"

	"bonanza.build/pkg/bazelclient/arguments"

	"github.com/stretchr/testify/require"
)

func TestParseCommandAndArguments(t *testing.T) {
	t.Run("NoArguments", func(t *testing.T) {
		// If no arguments are provided, Bazel defaults to
		// displaying help output.
		command, err := arguments.ParseCommandAndArguments(
			arguments.ConfigurationDirectives{},
			[]string{},
		)
		require.NoError(t, err)
		require.Equal(t, arguments.HelpFlags{
			HelpVerbosity: arguments.HelpVerbosity_Medium,
		}, command.(*arguments.HelpCommand).HelpFlags)
	})

	t.Run("CommandNotRecognized", func(t *testing.T) {
		_, err := arguments.ParseCommandAndArguments(
			arguments.ConfigurationDirectives{},
			[]string{
				"bquery",
				"--noinclude_aspects",
			},
		)
		require.EqualError(t, err, "command \"bquery\" not recognized")
	})

	t.Run("Build", func(t *testing.T) {
		t.Run("BuildTagFilters", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"--build_tag_filters=a,-b",
					"//...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, "a,-b", command.(*arguments.BuildCommand).BuildFlags.BuildTagFilters)
		})

		t.Run("KeepGoingLong", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"--keep_going",
					"//...",
				},
			)
			require.NoError(t, err)
			require.True(t, command.(*arguments.BuildCommand).BuildFlags.KeepGoing)
		})

		t.Run("KeepGoingLong0", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"--keep_going=0",
					"//...",
				},
			)
			require.NoError(t, err)
			require.False(t, command.(*arguments.BuildCommand).BuildFlags.KeepGoing)
		})

		t.Run("KeepGoingLong1", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"--keep_going=1",
					"//...",
				},
			)
			require.NoError(t, err)
			require.True(t, command.(*arguments.BuildCommand).BuildFlags.KeepGoing)
		})

		t.Run("KeepGoingLongFalse", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"--keep_going=false",
					"//...",
				},
			)
			require.NoError(t, err)
			require.False(t, command.(*arguments.BuildCommand).BuildFlags.KeepGoing)
		})

		t.Run("KeepGoingLongTrue", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"--keep_going=true",
					"//...",
				},
			)
			require.NoError(t, err)
			require.True(t, command.(*arguments.BuildCommand).BuildFlags.KeepGoing)
		})

		t.Run("KeepGoingLongNo", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"--keep_going=no",
					"//...",
				},
			)
			require.NoError(t, err)
			require.False(t, command.(*arguments.BuildCommand).BuildFlags.KeepGoing)
		})

		t.Run("KeepGoingLongYes", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"--keep_going=yes",
					"//...",
				},
			)
			require.NoError(t, err)
			require.True(t, command.(*arguments.BuildCommand).BuildFlags.KeepGoing)
		})

		t.Run("KeepGoingLongOther", func(t *testing.T) {
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"--keep_going=maybe",
					"//...",
				},
			)
			require.EqualError(t, err, "flag --keep_going only accepts \"true\", \"false\", \"yes\", \"no\", \"1\" or \"0\", not \"maybe\"")
		})

		t.Run("KeepGoingShort", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"-k",
					"//...",
				},
			)
			require.NoError(t, err)
			require.True(t, command.(*arguments.BuildCommand).BuildFlags.KeepGoing)
		})

		t.Run("NoKeepGoingLong", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"-k",
					"--nokeep_going",
					"//...",
				},
			)
			require.NoError(t, err)
			require.False(t, command.(*arguments.BuildCommand).BuildFlags.KeepGoing)
		})

		t.Run("NoKeepGoingShort", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"-k",
					"-k-",
					"//...",
				},
			)
			require.NoError(t, err)
			require.False(t, command.(*arguments.BuildCommand).BuildFlags.KeepGoing)
		})

		t.Run("NoKeepGoingUnexpectedValue", func(t *testing.T) {
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"-k",
					"--nokeep_going=123",
					"//...",
				},
			)
			require.EqualError(t, err, "flag --nokeep_going does not take a value")
		})

		t.Run("PositiveNegativePatterns", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"build",
					"--",
					"//...",
					"-//foo/...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, []string{
				"//...",
				"-//foo/...",
			}, command.(*arguments.BuildCommand).Arguments)
		})

		t.Run("ArgumentsInConfiguration", func(t *testing.T) {
			// The "--" argument can be used to stop
			// processing flags. However, should only apply
			// within a single directive.
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"build": [][]string{
						{
							"--",
							"//...",
							"-//doc/...",
						},
						{
							"--platforms",
							"@rules_go//go/toolchain:linux_amd64",
						},
					},
				},
				[]string{
					"build",
					"--keep_going",
				},
			)
			require.NoError(t, err)
			require.Equal(
				t,
				"@rules_go//go/toolchain:linux_amd64",
				command.(*arguments.BuildCommand).BuildFlags.Platforms,
			)
			require.Equal(t, []string{
				"//...",
				"-//doc/...",
			}, command.(*arguments.BuildCommand).Arguments)
		})
	})

	t.Run("Clean", func(t *testing.T) {
		t.Run("HelpVerbosityNotApplicableViaArguments", func(t *testing.T) {
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"clean",
					"--help_verbosity",
					"short",
				},
			)
			require.EqualError(t, err, "flag --help_verbosity does not apply to this command")
		})

		t.Run("HelpVerbosityNotApplicableViaConfigClean", func(t *testing.T) {
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"clean": [][]string{{
						"--help_verbosity",
						"short",
					}},
				},
				[]string{
					"clean",
				},
			)
			require.EqualError(t, err, "flag --help_verbosity does not apply to this command")
		})

		t.Run("HelpVerbosityNotApplicableViaConfigCommon", func(t *testing.T) {
			// In "common", it is permitted to place flags
			// that aren't necessarily applicable to the
			// current command.
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"common": [][]string{{
						"--help_verbosity",
						"short",
					}},
				},
				[]string{
					"clean",
				},
			)
			require.NoError(t, err)
		})

		t.Run("HelpVerbosityNotApplicableViaConfigAlways", func(t *testing.T) {
			// When placed in "always", we must throw errors
			// if flags aren't applicable.
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"always": [][]string{{
						"--help_verbosity",
						"short",
					}},
				},
				[]string{
					"clean",
				},
			)
			require.EqualError(t, err, "flag --help_verbosity does not apply to this command")
		})

		t.Run("ShortNotApplicable", func(t *testing.T) {
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"clean",
					"--short",
				},
			)
			require.EqualError(t, err, "flag --short does not apply to this command")
		})
	})

	t.Run("Help", func(t *testing.T) {
		t.Run("NoFlags", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"help",
				},
			)
			require.NoError(t, err)
			require.Equal(t, arguments.HelpFlags{
				HelpVerbosity: arguments.HelpVerbosity_Medium,
			}, command.(*arguments.HelpCommand).HelpFlags)
		})

		t.Run("Short", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"help",
					"--short",
				},
			)
			require.NoError(t, err)
			require.Equal(t, arguments.HelpFlags{
				HelpVerbosity: arguments.HelpVerbosity_Short,
			}, command.(*arguments.HelpCommand).HelpFlags)
		})

		t.Run("ShortUnexpectedValue", func(t *testing.T) {
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"help",
					"--short=123",
				},
			)
			require.EqualError(t, err, "flag --short does not take a value")
		})

		t.Run("DashL", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"help",
					"-l",
				},
			)
			require.NoError(t, err)
			require.Equal(t, arguments.HelpFlags{
				HelpVerbosity: arguments.HelpVerbosity_Long,
			}, command.(*arguments.HelpCommand).HelpFlags)
		})

		t.Run("HelpVerbosity", func(t *testing.T) {
			t.Run("Equals", func(t *testing.T) {
				command, err := arguments.ParseCommandAndArguments(
					arguments.ConfigurationDirectives{},
					[]string{
						"help",
						"--help_verbosity=short",
					},
				)
				require.NoError(t, err)
				require.Equal(t, arguments.HelpFlags{
					HelpVerbosity: arguments.HelpVerbosity_Short,
				}, command.(*arguments.HelpCommand).HelpFlags)
			})

			t.Run("Space", func(t *testing.T) {
				command, err := arguments.ParseCommandAndArguments(
					arguments.ConfigurationDirectives{},
					[]string{
						"help",
						"--help_verbosity",
						"long",
					},
				)
				require.NoError(t, err)
				require.Equal(t, arguments.HelpFlags{
					HelpVerbosity: arguments.HelpVerbosity_Long,
				}, command.(*arguments.HelpCommand).HelpFlags)
			})

			t.Run("MissingValue", func(t *testing.T) {
				_, err := arguments.ParseCommandAndArguments(
					arguments.ConfigurationDirectives{},
					[]string{
						"help",
						"--help_verbosity",
					},
				)
				require.EqualError(t, err, "flag --help_verbosity expects a value")
			})

			t.Run("UnknownValue", func(t *testing.T) {
				_, err := arguments.ParseCommandAndArguments(
					arguments.ConfigurationDirectives{},
					[]string{
						"help",
						"--help_verbosity",
						"large",
					},
				)
				require.EqualError(t, err, "flag --help_verbosity only accepts \"long\", \"medium\" or \"short\", not \"large\"")
			})
		})

		t.Run("WithCommandName", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"help",
					"build",
				},
			)
			require.NoError(t, err)
			require.Equal(
				t,
				[]string{"build"},
				command.(*arguments.HelpCommand).Arguments,
			)
		})
	})

	t.Run("Run", func(t *testing.T) {
		t.Run("PlatformsMostSpecific", func(t *testing.T) {
			// If multiple directives specify the same
			// flags, we should always prefer the one that
			// is most specific.
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"common": [][]string{{
						"--platforms",
						"@rules_go//go/toolchain:linux_amd64",
					}},
					"run": [][]string{{
						"--platforms",
						"@bazel_tools//tools:host_platform",
					}},
				},
				[]string{
					"run",
					"//cmd/my_tool",
					"--",
					"--help",
				},
			)
			require.NoError(t, err)
			require.Equal(
				t,
				"@bazel_tools//tools:host_platform",
				command.(*arguments.RunCommand).BuildFlags.Platforms,
			)
		})

		t.Run("RunUnderMultipleSameSpecificity", func(t *testing.T) {
			// If the same argument is provided at the same
			// specificity multiple times, the last value
			// should be applied.
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"run": [][]string{
						{"--run_under=echo"},
						{"--run_under=time"},
					},
				},
				[]string{
					"run",
					"//cmd/my_tool",
				},
			)
			require.NoError(t, err)
			require.Equal(
				t,
				"time",
				command.(*arguments.RunCommand).RunFlags.RunUnder,
			)
		})
	})

	t.Run("Version", func(t *testing.T) {
		t.Run("NoFlags", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"version",
				},
			)
			require.NoError(t, err)
			require.Equal(t, arguments.VersionFlags{
				GnuFormat: false,
			}, command.(*arguments.VersionCommand).VersionFlags)
		})

		t.Run("GNUFormatInConfigurationFile", func(t *testing.T) {
			// Perform an end to end test, where we have a
			// simple .bazelrc file in the home directory
			// that contains "version --gnu_format". When we
			// run "bazel version", this should cause it to
			// print just the program name and version
			// number.
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"version": [][]string{
						{"--gnu_format"},
					},
				},
				[]string{
					"version",
				},
			)
			require.NoError(t, err)
			require.Equal(t, arguments.VersionFlags{
				GnuFormat: true,
			}, command.(*arguments.VersionCommand).VersionFlags)
		})

		t.Run("GNUFormatBehindConfigFlag", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"version:foo": [][]string{
						{"--gnu_format"},
					},
				},
				[]string{
					"version",
					"--config=foo",
				},
			)
			require.NoError(t, err)
			require.Equal(t, arguments.VersionFlags{
				GnuFormat: true,
			}, command.(*arguments.VersionCommand).VersionFlags)
		})

		t.Run("InvalidConfig", func(t *testing.T) {
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				[]string{
					"version",
					"--config=foo",
				},
			)
			require.EqualError(t, err, "config value \"foo\" is not defined in any configuration file")
		})

		t.Run("CyclicConfig", func(t *testing.T) {
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"version:foo": [][]string{
						{"--config=bar"},
					},
					"version:bar": [][]string{
						{"--config=baz"},
					},
					"version:baz": [][]string{
						{"--config=foo"},
					},
				},
				[]string{
					"version",
					"--config=foo",
				},
			)
			require.EqualError(t, err, "config expansion for configuration directive \"version:foo\" contains a cycle")
		})
	})
}
