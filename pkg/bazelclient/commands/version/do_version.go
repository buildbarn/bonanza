package version

import (
	"fmt"

	"bonanza.build/pkg/bazelclient/arguments"
)

// DoVersion implements the "bazel version" command, which prints the
// tool's version number.
func DoVersion(args *arguments.VersionCommand) {
	// As we currently don't try to be compatible with an explicit
	// version of Bazel, mimic the output of a version of Bazel that
	// was built from source.
	if args.VersionFlags.GnuFormat {
		fmt.Println("bazel no_version")
	} else {
		fmt.Println("Build target: @@//src/main/java/com/google/devtools/build/lib/bazel:BazelServer")
		fmt.Println("Build time: Thu Jan 01 00:00:00 1970 (0)")
		fmt.Println("Build timestamp: Thu Jan 01 00:00:00 1970 (0)")
		fmt.Println("Build timestamp as int: 0")
	}
}
