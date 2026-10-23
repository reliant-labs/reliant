module github.com/reliant-labs/forge

go 1.27.0

toolchain go1.27.3

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260825204119-511051f7f437.2
	buf.build/go/protovalidate v1.4.0
	cel.dev/cel-go v0.32.0
	cloud.google.com/go/auth v0.23.2
	connectrpc.com/connect v1.21.1
	connectrpc.com/connect/v2 v2.0.0
	connectrpc.com/otelconnect v0.12.0
	connectrpc.com/vanguard v0.5.0
	github.com/BurntSushi/toml v1.6.0
	github.com/ClickHouse/clickhouse-go/v2 v2.49.0
	github.com/MicahParks/keyfunc/v3 v3.8.2
	github.com/XSAM/otelsql v0.44.0
	github.com/brianvoe/gofakeit/v7 v7.18.0
	github.com/ebitengine/purego v0.11.2
	github.com/evanw/esbuild v0.28.2
	github.com/fergusstrange/embedded-postgres v1.34.0
	github.com/fsnotify/fsnotify v1.10.1
	github.com/go-delve/delve v1.27.2
	github.com/gofrs/flock v0.13.1
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/golang-migrate/migrate/v4 v4.20.1
	github.com/google/pprof v0.0.0-20260604005048-7023385849c0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/jinzhu/inflection v1.0.0
	github.com/lib/pq v1.12.3
	github.com/mattn/go-sqlite3 v1.14.52
	github.com/nats-io/nats.go v1.54.0
	github.com/oklog/ulid/v2 v2.1.2
	github.com/open-telemetry/opentelemetry-collector-contrib/pkg/translator/pprof v0.163.0
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.1.1
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2
	github.com/prometheus/alertmanager v0.28.1
	github.com/prometheus/client_golang v1.25.0
	github.com/prometheus/common v0.72.0
	github.com/robfig/cron/v3 v3.0.1
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10
	github.com/tdewolff/parse/v2 v2.8.18
	github.com/uptrace/bun v1.3.0
	github.com/uptrace/bun/dialect/pgdialect v1.3.0
	github.com/uptrace/bun/extra/bunotel v1.3.0
	go.opentelemetry.io/collector/pdata v1.69.0
	go.opentelemetry.io/collector/pdata/pprofile v0.163.0
	go.opentelemetry.io/contrib/bridges/otelslog v0.21.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0
	go.opentelemetry.io/contrib/instrumentation/runtime v0.72.0
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.23.0
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.23.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.47.0
	go.opentelemetry.io/otel/exporters/prometheus v0.69.0
	go.opentelemetry.io/otel/log v1.47.0
	go.opentelemetry.io/otel/metric v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/sdk/log v1.47.0
	go.opentelemetry.io/otel/sdk/metric v1.47.0
	go.opentelemetry.io/otel/trace v1.47.0
	go.opentelemetry.io/proto/otlp v1.11.1
	go.temporal.io/sdk v1.50.0
	go.temporal.io/sdk/contrib/opentelemetry v0.8.1
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/mod v0.42.0
	golang.org/x/net v0.61.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.47.0
	golang.org/x/text v0.43.0
	golang.org/x/time v0.16.0
	golang.org/x/tools v0.52.0
	google.golang.org/genproto/googleapis/api v0.0.0-20260928230214-8a89bd6388cc
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
	gopkg.in/evanphx/json-patch.v4 v4.13.0
	gopkg.in/yaml.v3 v3.0.1
	k8s.io/api v0.37.1
	k8s.io/apiextensions-apiserver v0.37.1
	k8s.io/apimachinery v0.37.1
	k8s.io/client-go v0.37.1
	k8s.io/streaming v0.37.1
	kcl-lang.io/kcl-go v0.13.1
	kcl-lang.io/kpm v0.13.1
	kcl-lang.io/lib v0.13.1
	mvdan.cc/sh/v3 v3.14.1
	oras.land/oras-go/v2 v2.6.4
	sigs.k8s.io/controller-runtime v0.25.2
	sigs.k8s.io/controller-tools v0.23.0
	sigs.k8s.io/yaml v1.6.0
)

require (
	cel.dev/expr v0.25.3 // indirect
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	cloud.google.com/go/iam v1.13.0 // indirect
	cloud.google.com/go/monitoring v1.30.0 // indirect
	cloud.google.com/go/storage v1.66.0 // indirect
	dario.cat/mergo v1.0.2 // indirect
	github.com/Azure/go-ansiterm v0.0.0-20250102033503-faa5f7b0171c // indirect
	github.com/ClickHouse/ch-go v0.74.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/detectors/gcp v1.37.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric v0.61.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/internal/resourcemapping v0.61.0 // indirect
	github.com/MicahParks/jwkset v0.11.3 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/andybalholm/brotli v1.2.6 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/armon/go-metrics v0.3.10 // indirect
	github.com/aws/aws-sdk-go v1.55.5 // indirect
	github.com/aws/aws-sdk-go-v2 v1.45.1 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.20 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.33.2 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.2 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.19.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/s3 v1.110.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.8.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.36.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.41.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.48.0 // indirect
	github.com/aws/smithy-go v1.28.1 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bgentry/go-netrc v0.0.0-20140422174119-9fd32a8b3d3d // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/chai2010/jsonv v1.1.3 // indirect
	github.com/chainguard-dev/git-urls v1.0.2 // indirect
	github.com/cilium/ebpf v0.22.0 // indirect
	github.com/cloudflare/circl v1.6.5 // indirect
	github.com/cncf/xds/go v0.0.0-20260202195803-dba9d589def2 // indirect
	github.com/coder/quartz v0.1.2 // indirect
	github.com/containers/image/v5 v5.36.2 // indirect
	github.com/containers/libtrust v0.0.0-20230121012942-c1716e8a8d01 // indirect
	github.com/containers/ocicrypt v1.3.2 // indirect
	github.com/containers/storage v1.59.1 // indirect
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/cyphar/filepath-securejoin v0.7.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dchest/siphash v1.2.3 // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/cli v29.2.1+incompatible // indirect
	github.com/docker/distribution v2.8.3+incompatible // indirect
	github.com/docker/docker v28.5.2+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.9.9 // indirect
	github.com/docker/go-connections v0.8.1 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/dominikbraun/graph v0.23.0 // indirect
	github.com/elliotchance/orderedmap/v2 v2.7.0 // indirect
	github.com/emicklei/go-restful/v3 v3.13.0 // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/envoyproxy/go-control-plane/envoy v1.39.0 // indirect
	github.com/envoyproxy/protoc-gen-validate v1.3.3 // indirect
	github.com/evanphx/json-patch/v5 v5.9.11 // indirect
	github.com/facebookgo/clock v0.0.0-20150410010913-600d898af40a // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.3 // indirect
	github.com/go-faster/city v1.0.1 // indirect
	github.com/go-faster/errors v0.7.1 // indirect
	github.com/go-git/gcfg v1.5.1-0.20230307220236-3a3c6141e376 // indirect
	github.com/go-git/go-billy/v5 v5.9.1 // indirect
	github.com/go-git/go-git/v5 v5.19.2 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-openapi/jsonpointer v1.0.1 // indirect
	github.com/go-openapi/jsonreference v1.0.1 // indirect
	github.com/go-openapi/swag v0.29.2 // indirect
	github.com/go-openapi/swag/cmdutils v0.29.2 // indirect
	github.com/go-openapi/swag/conv v0.29.2 // indirect
	github.com/go-openapi/swag/fileutils v0.29.2 // indirect
	github.com/go-openapi/swag/jsonutils v0.29.2 // indirect
	github.com/go-openapi/swag/loading v0.29.2 // indirect
	github.com/go-openapi/swag/mangling v0.29.2 // indirect
	github.com/go-openapi/swag/netutils v0.29.2 // indirect
	github.com/go-openapi/swag/pools v0.29.2 // indirect
	github.com/go-openapi/swag/stringutils v0.29.2 // indirect
	github.com/go-openapi/swag/typeutils v0.29.2 // indirect
	github.com/go-openapi/swag/yamlutils v0.29.2 // indirect
	github.com/gobuffalo/flect v1.0.3 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/gofrs/uuid v4.4.0+incompatible // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang-collections/collections v0.0.0-20130729185459-604e922904d3 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/cel-go v0.31.0 // indirect
	github.com/google/gnostic-models v0.7.1 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.21 // indirect
	github.com/googleapis/gax-go/v2 v2.24.0 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware/v2 v2.3.3 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.31.0 // indirect
	github.com/hashicorp/aws-sdk-go-base/v2 v2.0.0-beta.74 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-getter v1.8.9 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/hashicorp/go-msgpack/v2 v2.1.1 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-sockaddr v1.0.7 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/hashicorp/memberlist v0.5.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgerrcode v0.0.0-20250907135507-afb5586c32a6 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jbenet/go-context v0.0.0-20150711004518-d14ea06fba99 // indirect
	github.com/jinzhu/copier v0.4.0 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/jpillora/backoff v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kevinburke/ssh_config v1.6.0 // indirect
	github.com/klauspost/compress v1.20.1 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/kubescape/go-git-url v0.0.33 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.4 // indirect
	github.com/mdlayher/socket v0.5.1 // indirect
	github.com/mdlayher/vsock v1.2.1 // indirect
	github.com/miekg/dns v1.1.41 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/moby/spdystream v0.5.1 // indirect
	github.com/moby/sys/capability v0.4.0 // indirect
	github.com/moby/sys/mountinfo v0.7.2 // indirect
	github.com/moby/sys/user v0.4.1 // indirect
	github.com/moby/term v0.5.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/mwitkow/go-conntrack v0.0.0-20190716064945-2f068394615f // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/nexus-rpc/nexus-proto-annotations v0.1.0 // indirect
	github.com/nexus-rpc/sdk-go v0.7.0 // indirect
	github.com/oklog/run v1.1.0 // indirect
	github.com/oklog/ulid v1.3.1 // indirect
	github.com/opencontainers/runtime-spec v1.3.0 // indirect
	github.com/otiai10/copy v1.14.1 // indirect
	github.com/otiai10/mint v1.6.3 // indirect
	github.com/paulmach/orb v0.13.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.27 // indirect
	github.com/pjbgf/sha1cd v0.6.0 // indirect
	github.com/planetscale/vtprotobuf v0.6.1-0.20240319094008-0393e58bdf10 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/exporter-toolkit v0.13.2 // indirect
	github.com/prometheus/otlptranslator v1.0.0 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/prometheus/sigv4 v0.1.0 // indirect
	github.com/puzpuzpuz/xsync/v3 v3.5.1 // indirect
	github.com/robfig/cron v1.2.0 // indirect
	github.com/sean-/seed v0.0.0-20170313163322-e2103e2c3529 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	github.com/shurcooL/httpfs v0.0.0-20230704072500-f1e31cf0ba5c // indirect
	github.com/shurcooL/vfsgen v0.0.0-20230704071429-0000e147ea92 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	github.com/skeema/knownhosts v1.3.3 // indirect
	github.com/spiffe/go-spiffe/v2 v2.8.1 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/thoas/go-funk v0.9.3 // indirect
	github.com/tmthrgd/go-hex v0.0.0-20190904060850-447a3041c3bc // indirect
	github.com/ulikunitz/xz v0.5.16 // indirect
	github.com/uptrace/opentelemetry-go-extra/otelsql v0.3.2 // indirect
	github.com/vbatts/tar-split v0.12.2 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/xanzy/ssh-agent v0.3.3 // indirect
	github.com/xi2/xz v0.0.0-20171230120015-48954b6210f8 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/collector/featuregate v1.69.0 // indirect
	go.opentelemetry.io/contrib/detectors/gcp v1.46.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.71.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	go.temporal.io/api v1.63.5 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/arch v0.30.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/telemetry v0.0.0-20261009151841-97e8ea77aa2a // indirect
	gomodules.xyz/jsonpatch/v2 v2.5.0 // indirect
	google.golang.org/api v0.297.0 // indirect
	google.golang.org/genproto v0.0.0-20260831171406-18b4a7587f8a // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/warnings.v0 v0.1.2 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260821135717-be32def86098 // indirect
	k8s.io/utils v0.0.0-20260707023825-cf1189d6abe3 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.2 // indirect
)

// ONE MODULE. The CLI (cmd/, internal/) and the app-runtime libraries
// (pkg/*: serverkit, appkit, orm, crud, testkit, ...) ship as a single module.
// pkg/ used to be its own module, github.com/reliant-labs/forge/pkg, released
// in lockstep with this one by `task release:forge` — which is to say the
// split bought nothing a single module does not give for free, while costing
// three hand-maintained syncs that kept rotting: this file's require on it
// went stale and made `go install .../cmd/forge@main` uninstallable, and the
// generator's list of forge/pkg symbols it emits went stale and let `forge
// generate` rewrite a project tree before failing. Merging them makes both
// classes unrepresentable: the generator and the runtime it generates calls
// into are now the same version by construction.
//
// Import paths did NOT change. A module at github.com/reliant-labs/forge with
// a pkg/testkit directory serves github.com/reliant-labs/forge/pkg/testkit —
// byte-identical to what the submodule served. Consumers changed one require
// line and no source.
//
// The cost, accepted deliberately: consumers inherit kcl-go, kpm, delve,
// esbuild and x/tools in their module GRAPH. Module-graph pruning means they
// never compile any of it and `go mod tidy` never adds it to their own
// requires; the weight is go.sum entries and MVS floors on deps already
// shared through pkg/. If that ever stops being worth it, the split can come
// back — but only with all three syncs DERIVED rather than hand-kept.
//
// There is deliberately NO `replace` here, for anything. Go refuses
// `go install <module>/cmd/...@version` for any module whose go.mod carries
// one, so a replace makes every published tag uninstallable — v0.0.3
// installed, v0.0.4 and v0.0.5 did not, and control-plane's CI pinned itself
// to v0.0.3 as a result. internal/modguard guards it.
//
// Release: `task release:forge -- vX.Y.Z` — one tag, one commit.
