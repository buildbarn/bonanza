package license

import (
	_ "embed" // Used to embed the license into the binary.
	"os"
)

//go:embed LICENSE
var licenseData []byte

// DoLicense implements the "bazel license" command, which prints the
// license agreement under which the tool is distributed.
func DoLicense() {
	os.Stdout.Write(licenseData)
}
