package tools

import (
	// Used by //:buildifier.
	_ "github.com/bazelbuild/buildtools/buildifier"

	// Used by CI.
	_ "golang.org/x/lint"
	// Used by rules_go's Protobuf rules.
	_ "google.golang.org/grpc/cmd/protoc-gen-go-grpc"

	// Used by CI.
	_ "mvdan.cc/gofumpt"
)
