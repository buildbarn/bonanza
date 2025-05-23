#!/bin/sh

set -eu

cd "$(dirname "$0")"

# Go dependencies
find . bazel-bin/pkg/proto -name '*.pb.go' -delete || true
bazel build -k $(bazel query --output=label 'kind("go_proto_library", //...)') || true
find bazel-bin/pkg/proto -name '*.pb.go' | while read f; do
  cat $f > $(echo $f | sed -e 's|.*/pkg/proto/|pkg/proto/|')
done
#go get -d -u ./... || true
go mod tidy || true

# Gazelle
find . -name '*.pb.go' -delete
rm -f $(find . -name '*.proto' | sed -e 's/[^/]*$/BUILD.bazel/')
bazel run //:gazelle

# bzlmod
bazel mod tidy

# Go
bazel run @cc_mvdan_gofumpt//:gofumpt -- -w -extra $(pwd)

# Protobuf
find . -name '*.proto' -exec bazel run @llvm_toolchain_llvm//:bin/clang-format -- -i {} +

# Generated .pb.go files
find bazel-bin/pkg/proto -name '*.pb.go' -delete || true
bazel build $(bazel query --output=label 'kind("go_proto_library", //...)')
find bazel-bin/pkg/proto -name '*.pb.go' | while read f; do
  cat $f > $(echo $f | sed -e 's|.*/pkg/proto/|pkg/proto/|')
done

# Files embedded into Go binaries
bazel build $(git grep '^[[:space:]]*//go:embed ' | sed -e 's|\(.*\)/.*//go:embed |//\1:|; s|"||g; s| .*||' | sort -u)
git grep '^[[:space:]]*//go:embed ' | sed -e 's|\(.*\)/.*//go:embed |\1/|' | while read o; do
  if [ -e "bazel-bin/$o" ]; then
    rm -rf "$o"
    cp -r "bazel-bin/$o" "$o"
    find "$o" -type f -exec chmod -x {} +
  fi
done
