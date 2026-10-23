module bonanza.build

go 1.27.3

// rules_go doesn't support gomock's package mode.
replace go.uber.org/mock => go.uber.org/mock v0.4.0

// Existing patches don't apply against newer go-fuse.
replace github.com/hanwen/go-fuse/v2 => github.com/hanwen/go-fuse/v2 v2.5.1

// Stick to a very specific version of Go Starlark to make patches apply.
replace go.starlark.net => go.starlark.net v0.0.0-20250225190231-0d3f41d403af

require (
	filippo.io/edwards25519 v1.2.0
	github.com/bazelbuild/buildtools v0.0.0-20260904073137-eaa4d125b423
	github.com/bluekeyes/go-gitdiff v0.9.0
	github.com/buildbarn/bb-remote-execution v0.0.0-20261022081640-1fbffdc41779
	github.com/buildbarn/bb-storage v0.0.0-20261023030055-a00415f5b9ce
	github.com/buildbarn/go-cdc v0.0.10
	github.com/ericlagergren/siv v0.0.0-20220507050439-0b757b3aa5f1
	github.com/google/uuid v1.6.0
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51
	github.com/prometheus/client_golang v1.25.0
	github.com/stretchr/testify v1.12.1
	github.com/ulikunitz/xz v0.5.17
	go.starlark.net v0.0.0-20261021141710-4f4267fb140d
	go.uber.org/mock v0.6.0
	golang.org/x/exp v0.0.0-20261014143641-f0251eb4aa3e
	golang.org/x/lint v0.0.0-20241112194109-818c5a804067
	golang.org/x/sync v0.24.0
	golang.org/x/term v0.47.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261019180522-ce908d29299b
	google.golang.org/grpc v1.84.0
	google.golang.org/grpc/security/advancedtls v1.0.0
	google.golang.org/protobuf v1.36.12
	maragu.dev/gomponents v1.4.0
	mvdan.cc/gofumpt v0.12.0
)

require (
	cel.dev/expr v0.25.3 // indirect
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.24.1 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.3.0 // indirect
	cloud.google.com/go/compute/metadata v0.10.0 // indirect
	cloud.google.com/go/iam v1.15.0 // indirect
	cloud.google.com/go/longrunning v1.4.0 // indirect
	cloud.google.com/go/monitoring v1.32.0 // indirect
	cloud.google.com/go/storage v1.70.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric v0.62.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/internal/resourcemapping v0.62.0 // indirect
	github.com/aohorodnyk/mimeheader v0.0.6 // indirect
	github.com/aws/aws-sdk-go-v2 v1.47.4 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.24 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.33.10 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.10 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.22 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.10.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.38.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.43.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.5 // indirect
	github.com/aws/smithy-go v1.28.6 // indirect
	github.com/bazelbuild/remote-apis v0.0.0-20261020142950-c2c07e3d718d // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/buildbarn/go-sha256tree v0.0.0-20250310211320-0f70f20e855b // indirect
	github.com/buildbarn/go-xdr v0.0.0-20261005144839-a2095890273e // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cncf/xds/go v0.0.0-20260202195803-dba9d589def2 // indirect
	github.com/envoyproxy/go-control-plane/envoy v1.40.0 // indirect
	github.com/envoyproxy/protoc-gen-validate v1.3.3 // indirect
	github.com/ericlagergren/polyval v0.0.0-20230805202542-18692a1b76f9 // indirect
	github.com/ericlagergren/subtle v0.0.0-20220507045147-890d697da010 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/go-jose/go-jose/v3 v3.0.5 // indirect
	github.com/go-jose/go-jose/v4 v4.1.6 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/go-jsonnet v0.22.0 // indirect
	github.com/google/s2a-go v0.1.11 // indirect
	github.com/google/safeopen v0.0.0-20260327150837-43626d6f4685 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.24 // indirect
	github.com/googleapis/gax-go/v2 v2.27.0 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware v1.4.0 // indirect
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.32.0 // indirect
	github.com/hanwen/go-fuse/v2 v2.12.1 // indirect
	github.com/jhump/protoreflect/v2 v2.0.0 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/klauspost/cpuid/v2 v2.4.1 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/planetscale/vtprotobuf v0.6.1-0.20240319094008-0393e58bdf10 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.72.1 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/sercand/kuberesolver/v5 v5.1.1 // indirect
	github.com/spiffe/go-spiffe/v2 v2.9.0 // indirect
	github.com/winfsp/go-winfsp v1.0.6 // indirect
	github.com/zeebo/blake3 v0.2.4 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/detectors/gcp v1.47.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.72.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0 // indirect
	go.opentelemetry.io/contrib/propagators/b3 v1.47.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/jaeger v1.17.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.1 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	google.golang.org/api v0.308.0 // indirect
	google.golang.org/genproto v0.0.0-20261019180522-ce908d29299b // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261019180522-ce908d29299b // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)
