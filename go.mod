module bonanza.build

go 1.27.4

// rules_go doesn't support gomock's package mode.
replace go.uber.org/mock => go.uber.org/mock v0.4.0

// v0.26.0 is not in BCR yet.
replace cel.dev/expr => cel.dev/expr v0.25.3

require (
	filippo.io/edwards25519 v1.2.0
	github.com/bazelbuild/buildtools v0.0.0-20260904073137-eaa4d125b423
	github.com/bluekeyes/go-gitdiff v0.9.0
	github.com/buildbarn/bb-remote-execution v0.0.0-20261120202529-e9f6d456803e
	github.com/buildbarn/bb-storage v0.0.0-20261122143634-523f89e0623c
	github.com/buildbarn/go-cdc v0.0.10
	github.com/ericlagergren/siv v0.0.0-20220507050439-0b757b3aa5f1
	github.com/google/uuid v1.6.0
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51
	github.com/klauspost/compress v1.20.5
	github.com/prometheus/client_golang v1.25.0
	github.com/stretchr/testify v1.12.1
	github.com/ulikunitz/xz v0.5.17
	go.uber.org/mock v0.6.0
	golang.org/x/exp v0.0.0-20261120182113-8801fbdfac81
	golang.org/x/lint v0.0.0-20241112194109-818c5a804067
	golang.org/x/sync v0.25.0
	golang.org/x/term v0.48.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261118221215-c8830f628b3f
	google.golang.org/grpc v1.85.0
	google.golang.org/grpc/security/advancedtls v1.0.0
	google.golang.org/protobuf v1.36.12
	maragu.dev/gomponents v1.4.0
	mvdan.cc/gofumpt v0.12.0
)

require (
	cloud.google.com/go/compute/metadata v0.10.0 // indirect
	cloud.google.com/go/longrunning v1.5.0 // indirect
	github.com/aohorodnyk/mimeheader v0.0.6 // indirect
	github.com/bazelbuild/remote-apis v0.0.0-20261110225606-e0c0554bfc8a // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/buildbarn/go-sha256tree v0.0.0-20250310211320-0f70f20e855b // indirect
	github.com/buildbarn/go-xdr v0.0.0-20261005144839-a2095890273e // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/ericlagergren/polyval v0.0.0-20230805202542-18692a1b76f9 // indirect
	github.com/ericlagergren/subtle v0.0.0-20220507045147-890d697da010 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/fxtlabs/primes v0.0.0-20150821004651-dad82d10a449 // indirect
	github.com/go-jose/go-jose/v3 v3.0.5 // indirect
	github.com/go-jose/go-jose/v4 v4.1.9 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/go-jsonnet v0.22.0 // indirect
	github.com/google/safeopen v0.0.0-20260327150837-43626d6f4685 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware v1.4.0 // indirect
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.34.0 // indirect
	github.com/hanwen/go-fuse/v2 v2.12.1 // indirect
	github.com/jhump/protoreflect/v2 v2.0.1 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/klauspost/cpuid/v2 v2.4.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pandatix/go-abnf v0.5.5 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.74.0 // indirect
	github.com/prometheus/procfs v0.23.0 // indirect
	github.com/sercand/kuberesolver/v5 v5.1.1 // indirect
	github.com/spiffe/go-spiffe/v2 v2.9.0 // indirect
	github.com/winfsp/go-winfsp v1.0.6 // indirect
	github.com/zeebo/blake3 v0.2.4 // indirect
	go.opentelemetry.io/auto/sdk v1.2.2 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.72.0 // indirect
	go.opentelemetry.io/contrib/propagators/b3 v1.47.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/jaeger v1.17.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.1 // indirect
	go.yaml.in/yaml/v2 v2.4.5 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/mod v0.43.0 // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/oauth2 v0.38.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/tools v0.53.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261118221215-c8830f628b3f // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)
