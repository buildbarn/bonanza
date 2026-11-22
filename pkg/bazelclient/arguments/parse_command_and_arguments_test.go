package arguments_test

import (
	"testing"

	"bonanza.build/pkg/bazelclient/arguments"
	"bonanza.build/pkg/label"

	"github.com/stretchr/testify/require"
)

func TestParseCommandAndArguments(t *testing.T) {
	t.Run("NoArguments", func(t *testing.T) {
		// If no arguments are provided, Bazel defaults to
		// displaying help output.
		command, err := arguments.ParseCommandAndArguments(
			arguments.ConfigurationDirectives{},
			"linux",
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
			"linux",
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
				"linux",
				[]string{
					"build",
					"--build_tag_filters=a,-b",
					"//...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, "a,-b", command.(*arguments.BuildCommand).BuildFlags.BuildTagFilters)
		})

		t.Run("HostPlatformDefault", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				"linux",
				[]string{
					"build",
					"//...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, "@platforms//host", command.(*arguments.BuildCommand).BuildFlags.HostPlatform)
		})

		t.Run("HostPlatform", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				"linux",
				[]string{
					"build",
					"--host_platform=//platforms:linux_amd64",
					"//...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, "//platforms:linux_amd64", command.(*arguments.BuildCommand).BuildFlags.HostPlatform)
		})

		t.Run("KeepGoingLong", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
				[]string{
					"clean",
				},
			)
			require.EqualError(t, err, "flag --help_verbosity does not apply to this command")
		})

		t.Run("ShortNotApplicable", func(t *testing.T) {
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
					"linux",
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
					"linux",
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
					"linux",
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
					"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
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
				"linux",
				[]string{
					"version",
					"--config=foo",
				},
			)
			require.EqualError(t, err, "config expansion for configuration directive \"version:foo\" contains a cycle")
		})
	})

	t.Run("PlatformSpecificConfig", func(t *testing.T) {
		directives := arguments.ConfigurationDirectives{
			"build": [][]string{
				{"--platforms=//platforms:rc"},
			},
			"build:linux": [][]string{
				{"--platforms=//platforms:linux"},
			},
			"build:macos": [][]string{
				{"--platforms=//platforms:macos"},
			},
			"build:enable": [][]string{
				{"--enable_platform_specific_config"},
			},
		}

		t.Run("Disabled", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				directives,
				"linux",
				[]string{
					"build",
					"//...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, "//platforms:rc", command.(*arguments.BuildCommand).BuildFlags.Platforms)
		})

		t.Run("EnabledOnCommandLine", func(t *testing.T) {
			// The platform specific config should be
			// applied after bazelrc directives, and
			// positional arguments should not be
			// duplicated by the second parsing pass.
			command, err := arguments.ParseCommandAndArguments(
				directives,
				"macos",
				[]string{
					"build",
					"//...",
					"--enable_platform_specific_config",
				},
			)
			require.NoError(t, err)
			require.Equal(t, "//platforms:macos", command.(*arguments.BuildCommand).BuildFlags.Platforms)
			require.Equal(t, []string{"//..."}, command.(*arguments.BuildCommand).Arguments)
		})

		t.Run("EnabledInConfigurationFile", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"common": [][]string{
						{"--enable_platform_specific_config"},
					},
					"build:linux": [][]string{
						{"--platforms=//platforms:linux"},
					},
				},
				"linux",
				[]string{
					"build",
					"//...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, "//platforms:linux", command.(*arguments.BuildCommand).BuildFlags.Platforms)
		})

		t.Run("EnabledThroughConfigFlag", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				directives,
				"linux",
				[]string{
					"build",
					"--config=enable",
					"//...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, "//platforms:linux", command.(*arguments.BuildCommand).BuildFlags.Platforms)
		})

		t.Run("CommandLineTakesPrecedence", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				directives,
				"linux",
				[]string{
					"build",
					"--enable_platform_specific_config",
					"--platforms=//platforms:command_line",
					"//...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, "//platforms:command_line", command.(*arguments.BuildCommand).BuildFlags.Platforms)
		})

		t.Run("NoDirectivesForPlatform", func(t *testing.T) {
			// Unlike --config=windows, it should not be an
			// error if no directives for the platform exist.
			command, err := arguments.ParseCommandAndArguments(
				directives,
				"windows",
				[]string{
					"build",
					"--enable_platform_specific_config",
					"//...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, "//platforms:rc", command.(*arguments.BuildCommand).BuildFlags.Platforms)
		})

		t.Run("UnknownPlatform", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				directives,
				"",
				[]string{
					"build",
					"--enable_platform_specific_config",
					"//...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, "//platforms:rc", command.(*arguments.BuildCommand).BuildFlags.Platforms)
		})

		t.Run("CommonFlagNotApplicableToCommand", func(t *testing.T) {
			// Flags in "common:linux" that don't apply to
			// the current command should be ignored, just
			// like for regular --config flags.
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"common:linux": [][]string{
						{"--keep_going"},
					},
				},
				"linux",
				[]string{
					"version",
					"--enable_platform_specific_config",
				},
			)
			require.NoError(t, err)
			require.True(t, command.(*arguments.VersionCommand).CommonFlags.EnablePlatformSpecificConfig)
		})
	})

	t.Run("UserDefinedBuildSettings", func(t *testing.T) {
		t.Run("Success", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"build": [][]string{
						{"--@rules_go//go/config:race"},
					},
					"common": [][]string{
						{"--//my/settings:from_rc=hello"},
					},
				},
				"linux",
				[]string{
					"build",
					"--@rules_go//go/config:pure=true",
					"--//my/settings:list=a,b",
					"--no@rules_python//python/config_settings:bootstrap_impl_enabled",
					"--no//my/settings:negated",
					"--@@canonical+//:setting=42",
					"//...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, []arguments.BuildSettingOverride{
				{Label: label.MustNewAbsoluteLabel("//my/settings:from_rc"), Value: "hello"},
				{Label: label.MustNewAbsoluteLabel("@rules_go//go/config:race"), Value: "true"},
				{Label: label.MustNewAbsoluteLabel("@rules_go//go/config:pure"), Value: "true"},
				{Label: label.MustNewAbsoluteLabel("//my/settings:list"), Value: "a,b"},
				{Label: label.MustNewAbsoluteLabel("@rules_python//python/config_settings:bootstrap_impl_enabled"), Value: "false"},
				{Label: label.MustNewAbsoluteLabel("//my/settings:negated"), Value: "false"},
				{Label: label.MustNewAbsoluteLabel("@@canonical+//:setting"), Value: "42"},
			}, command.(*arguments.BuildCommand).BuildSettingOverrides)
			require.Equal(t, []string{"//..."}, command.(*arguments.BuildCommand).Arguments)
		})

		t.Run("ValueNotConsumedFromNextArgument", func(t *testing.T) {
			// Like Bazel, the value must be provided using
			// "=". Otherwise the next argument is treated
			// as a target pattern.
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				"linux",
				[]string{
					"build",
					"--//my/settings:flag",
					"//...",
				},
			)
			require.NoError(t, err)
			require.Equal(t, []arguments.BuildSettingOverride{
				{Label: label.MustNewAbsoluteLabel("//my/settings:flag"), Value: "true"},
			}, command.(*arguments.BuildCommand).BuildSettingOverrides)
			require.Equal(t, []string{"//..."}, command.(*arguments.BuildCommand).Arguments)
		})

		t.Run("NegatedWithValue", func(t *testing.T) {
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				"linux",
				[]string{
					"build",
					"--no//my/settings:flag=true",
				},
			)
			require.EqualError(t, err, "flag --no//my/settings:flag does not take a value")
		})

		t.Run("NotApplicable", func(t *testing.T) {
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				"linux",
				[]string{
					"version",
					"--@rules_go//go/config:pure",
				},
			)
			require.EqualError(t, err, "flag --@rules_go//go/config:pure does not apply to this command")
		})

		t.Run("IgnoredInCommonDirective", func(t *testing.T) {
			command, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{
					"common": [][]string{
						{"--@rules_go//go/config:pure"},
					},
				},
				"linux",
				[]string{
					"version",
				},
			)
			require.NoError(t, err)
			require.Empty(t, command.(*arguments.VersionCommand).BuildSettingOverrides)
		})

		t.Run("InvalidLabel", func(t *testing.T) {
			// Flags that look like labels, but aren't valid
			// absolute labels, are not recognized.
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				"linux",
				[]string{
					"build",
					"--//foo/...=1",
				},
			)
			require.EqualError(t, err, "flag --//foo/... not recognized")
		})

		t.Run("NotALabel", func(t *testing.T) {
			_, err := arguments.ParseCommandAndArguments(
				arguments.ConfigurationDirectives{},
				"linux",
				[]string{
					"build",
					"--nonexistent_flag",
				},
			)
			require.EqualError(t, err, "flag --nonexistent_flag not recognized")
		})
	})
}
