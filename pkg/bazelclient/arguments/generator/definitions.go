// Copyright The Bazel Authors. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

var enumTypes = map[string][]string{
	"Color": {
		"yes",
		"no",
		"auto",
	},
	"HelpVerbosity": {
		"long",
		"medium",
		"short",
	},
	"LockfileMode": {
		"off",
		"update",
		"refresh",
		"error",
	},
}

var startupFlags = []flag{
	{
		longName:    "bazelrc",
		description: "The location of the user .bazelrc file containing default values of Bazel options. /dev/null indicates that all further `--bazelrc`s will be ignored, which is useful to disable the search for a user rc file, e.g. in release builds. This option can also be specified multiple times. E.g. with `--bazelrc=x.rc --bazelrc=y.rc --bazelrc=/dev/null --bazelrc=z.rc`, 1) x.rc and y.rc are read. 2) z.rc is ignored due to the prior /dev/null. If unspecified, Bazel uses the first .bazelrc file it finds in the following two locations: the workspace directory, then the user's home directory. Note: command line options will always supersede any option in bazelrc.",
		flagType:    stringListFlagType{},
	},
	{
		longName:    "home_rc",
		description: "Whether or not to look for the home bazelrc file at $HOME/.bazelrc.",
		flagType: boolFlagType{
			defaultValue: true,
		},
	},
	{
		longName:    "ignore_all_rc_files",
		description: "Disables all rc files, regardless of the values of other rc-modifying flags, even if these flags come later in the list of startup options.",
		flagType: boolFlagType{
			defaultValue: false,
		},
	},
	{
		longName:    "system_rc",
		description: "Whether or not to look for the system-wide bazelrc.",
		flagType: boolFlagType{
			defaultValue: true,
		},
	},
	{
		longName:    "workspace_rc",
		description: "Whether or not to look for the workspace bazelrc file at $workspace/.bazelrc.",
		flagType: boolFlagType{
			defaultValue: true,
		},
	},
}

var commonFlags = []flag{
	{
		longName:    "browser_url",
		description: "URL at which the Bonanza Browser service is hosted. This causes command line output to contain clickable links to the Bonanza Browser service.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "build_request_id",
		description: "Unique identifier, in UUID format, for the build being run.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "builtins_module",
		description: "Names of modules containing Starlark code that should be loaded implicitly.",
		flagType:    stringListFlagType{},
	},
	{
		longName:    "color",
		description: "Use terminal controls to colorize output.",
		flagType: enumFlagType{
			enumType:     "Color",
			defaultValue: "auto",
		},
	},
	{
		longName:    "ignore_dev_dependency",
		description: "If true, Bazel ignores `bazel_dep` and `use_extension` declared as `dev_dependency` in the MODULE.bazel of the root module. Note that, those dev dependencies are always ignored in the MODULE.bazel if it's not the root module regardless of the value of this flag.",
		flagType: boolFlagType{
			defaultValue: false,
		},
	},
	{
		longName:    "invocation_id",
		description: "Unique identifier, in UUID format, for the command being run.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "lockfile_mode",
		description: "Specifies how and whether or not to use the lockfile. Valid values are `update` to use the lockfile and update it if there are changes, `refresh` to additionally refresh mutable information (yanked versions and previously missing modules) from remote registries from time to time, `error` to use the lockfile but throw an error if it's not up-to-date, or `off` to neither read from or write to the lockfile.",
		flagType: enumFlagType{
			enumType:     "LockfileMode",
			defaultValue: "update",
		},
	},
	{
		longName:    "override_module",
		description: "Override a module with a local path in the form of <module name>=<path>. If the given path is an absolute path, it will be used as it is. If the given path is a relative path, it is relative to the current working directory. If the given path starts with '%workspace%, it is relative to the workspace root, which is the output of `bazel info workspace`. If the given path is empty, then remove any previous overrides.",
		flagType:    stringListFlagType{},
	},
	{
		longName:    "registry",
		description: "Specifies the registries to use to locate Bazel module dependencies. The order is important: modules will be looked up in earlier registries first, and only fall back to later registries when they're missing from the earlier ones.",
		flagType:    stringListFlagType{},
	},
	{
		longName:    "remote_cache",
		description: "A URI of a bonanza_storage_frontend endpoint. The supported schemas are grpc, grpcs (grpc with TLS enabled) and unix (local UNIX sockets). Specify grpc:// or unix: schema to disable TLS.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "remote_cache_compression",
		description: "If enabled, compress the contents of files using the \"simple LZW\" algorithm prior to uploading them to storage.",
		flagType: boolFlagType{
			defaultValue: true,
		},
	},
	{
		longName:    "remote_encryption_key",
		description: "A 128, 192 or 256 bit AES key that is used to encrypt files and directories prior to uploading them to storage.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "remote_executor",
		description: "A URI of a bonanza_scheduler endpoint. The supported schemas are grpc, grpcs (grpc with TLS enabled) and unix (local UNIX sockets). Specify grpc:// or unix: schema to disable TLS.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "remote_executor_builder_pkix_public_key",
		description: "The PKIX public key of the bonanza_builder processes to which to send builds.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "remote_executor_client_private_key",
		description: "Path of a file containing an elliptic-curve private key that is used to encrypt build requests that are submitted to bonanza_scheduler.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "remote_executor_client_certificate_chain",
		description: "Path of a file containing a certificate chain that corresponds to the elliptic-curve private key that is used to encrypt build requests that are submitted to bonanza_scheduler.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "remote_executor_fetcher_pkix_public_key",
		description: "The PKIX public key of the bonanza_fetcher processes to which to send requests to fetch Bazel module dependencies and files needed by repository rules.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "remote_instance_name",
		description: "Value to pass as instance_name in the remote execution API.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "repo_platform",
		description: "A label of a platform() target that is used to determine the platform that is used to execute repository rules and module extensions. If this argument is not provided, repository rules and module extensions cannot be evaluated.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "rule_implementation_wrapper_identifier",
		description: "Name of the Starlark function to invoke to wrap the execution of rule implementation functions. This can be used to decorate ctx to contain fields that are either deprecated, or trivially implementable in pure Starlark.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "subrule_implementation_wrapper_identifier",
		description: "Name of the Starlark function to invoke to wrap the execution of subrule implementation functions. This can be used to decorate ctx to contain fields that are either deprecated, or trivially implementable in pure Starlark.",
		flagType:    stringFlagType{},
	},
	{
		longName:    "xcode_version",
		description: "If specified, uses Xcode of the given version for relevant build actions. If unspecified, uses the executor default version of Xcode.",
		flagType:    buildSettingFlagType{},
	},
}

var commands = map[string]command{
	"build": {
		ancestor: "common",
		flags: []flag{
			{
				longName:    "keep_going",
				shortName:   "k",
				description: "Continue as much as possible after an error. While the target that failed and those that depend on it cannot be analyzed, other prerequisites of these targets can be.",
				flagType: boolFlagType{
					defaultValue: false,
				},
			},
			{
				longName:    "protocopt",
				description: "Additional options to pass to the protobuf compiler.",
				flagType:    buildSettingFlagType{},
			},
			{
				longName:    "platforms",
				description: "The labels of the platform rules describing the target platforms for the current command.",
				flagType:    stringFlagType{},
			},
		},
		takesArguments: true,
	},
	"clean": {
		ancestor: "build",
	},
	"help": {
		ancestor: "common",
		flags: []flag{
			{
				longName:    "help_verbosity",
				description: "Select the verbosity of the help command.",
				flagType: enumFlagType{
					enumType:     "HelpVerbosity",
					defaultValue: "medium",
				},
			},
			{
				longName:    "long",
				shortName:   "l",
				description: "Show full description of each option, instead of just its name.",
				flagType: expansionFlagType{
					expandsTo: []string{
						"--help_verbosity=long",
					},
				},
			},
			{
				longName:    "short",
				description: "Show only the names of the options, not their types or meanings.",
				flagType: expansionFlagType{
					expandsTo: []string{
						"--help_verbosity=short",
					},
				},
			},
		},
		takesArguments: true,
	},
	"info": {
		ancestor: "build",
		flags: []flag{
			{
				longName:    "show_make_env",
				description: "Include the \"Make\" environment in the output.",
				flagType:    boolFlagType{},
			},
		},
		takesArguments: true,
	},
	"license": {
		ancestor: "common",
	},
	"run": {
		ancestor: "build",
		flags: []flag{
			{
				longName:    "run_under",
				description: "Prefix to insert before the executables for the 'test' and 'run' commands. If the value is 'foo -bar', and the execution command line is 'test_binary -baz', then the final command line is 'foo -bar test_binary -baz'.This can also be a label to an executable target. Some examples are: 'valgrind', 'strace', 'strace -c', 'valgrind --quiet --num-callers=20', '//package:target', '//package:target --options'.",
				flagType:    stringFlagType{},
			},
		},
		takesArguments: true,
	},
	"version": {
		ancestor: "common",
		flags: []flag{
			{
				longName:    "gnu_format",
				description: "If set, write the version to stdout using the conventions described in the GNU standards.",
				flagType:    boolFlagType{},
			},
		},
	},
}
