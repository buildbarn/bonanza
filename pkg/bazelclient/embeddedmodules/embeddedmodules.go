// Package embeddedmodules embeds the Starlark modules that are
// uploaded by bonanza_bazel as part of every build. These modules
// provide functionality that Bazel implements natively, such as
// DefaultInfo, filegroup() and cc_binary().
//
// As these modules are stored inside this repository, any BUILD and
// BUILD.bazel files contained in them are named BUILD.tools. This
// prevents Bazel from interpreting them as packages of this
// repository. bonanza_bazel renames them back to BUILD.bazel when
// uploading.
package embeddedmodules

import (
	"embed"
)

// Modules contains the Starlark modules that are embedded into
// bonanza_bazel. Each top-level directory corresponds to a module.
//
//go:embed bazel_tools
//go:embed builtins_bzl
//go:embed builtins_core
var Modules embed.FS
