"""
What reliant RUNS — every workload, declared ONCE. Each environment's
`deploy/kcl/<env>/main.k` binds these to a runtime and states only what that
environment knows (ports, DSNs, which runtime).

YOURS. forge scaffolds this file once (when it is absent) and never rewrites
it; `forge scaffold` appends a declaration for a component it finds no
workload serving.

THE PROCESSES ARE THE ROOTS. reliant is one binary whose processes are the
composition roots in internal/app (//forge:root): `reliant server api`,
`server worker` and `server gateway` (research/forge-migration/09-roots-plan.md).
Every proto service mounts in the API root and every workflow activity runs in
the worker root, so a workload per proto service — what forge would scaffold
without this file — would declare processes that do not exist. Each workload
binds its root with `config_gen.root_of`, which sets the args that run it, and
an env projects exactly the config that root reaches with
`config_gen.workloadEnvMap`. `serves` lists the components a root mounts, so
`forge lint` and `forge scaffold` count them as deployed.

WHAT EVERY ENV RUNS, NOT ONLY THIS ONE. control-plane binds these same
declarations on its clusters by importing this file (kcl.mod
`[forge_projects]`, `import reliant.workloads`;
research/forge-migration/15-deployment-model-plan.md, M3), so each declaration carries the facts that are true of the PROCESS
wherever a pod runs it: its release contracts, probes, resource floor,
GOMEMLIMIT percent, scratch volumes and scheduling class. What a deployment
knows — the registry image, the command a prebuilt image runs, replicas,
prod's resource tier — stays in the binding that knows it. A host env takes
what a pod needs back off (dev/main.k `_host_only`): forge refuses
`volumes` and `priorityClassName` on a local process by name.
"""

import forge
import forge.workloads as fw
import config_gen

# -trimpath is declared here because forge (#2690) passes a GoBuild's flags,
# and only those, to every go build it runs from it: `forge build`, go-run (the
# dev migrate job, `-D host_runner=go-run`) and air's $FORGE_GO_BUILD_FLAGS.
# The Taskfile's GOFLAGS does not reach a process forge launches.
# scripts/air-build.sh passes -trimpath on its own as well.
_build = forge.GoBuild {cmd = "./cmd/reliant", output_name = "reliant", flags = ["-trimpath"]}

# ── Facts true of every reliant pod ─────────────────────────────────────────

# The port every reliant root's health server binds (HEALTH_PORT) and every
# reliant probe targets. It is a separate listener from the serving port
# (reliant-api-server's :9090 is a Connect/gRPC mux), so the probes name it
# rather than default to a port called `http`. `server api` and `server
# worker` default HEALTH_PORT to 8081; `server gateway` defaults it to 8080
# (gateway_config.proto), so an env that runs the gateway in a pod sets its
# health_port to this constant.
HEALTH_PORT = 8081

# /ready checks dependencies and gates traffic (it never restarts anything);
# /health only restarts. Readiness starts at once: it is a question, and "not
# ready yet" is a true answer. forge derives the liveness timings from these
# (initial delay = 0 + 10 x 3 + 15s margin = 45s), so a slow cold start
# raises the period or the failures, never a liveness knob of its own.
PROBES = fw.Probes {
    port = HEALTH_PORT
    readinessPath = "/ready"
    livenessPath = "/health"
    initialDelaySeconds = 0
    periodSeconds = 10
    timeoutSeconds = 3
    failureThreshold = 3
}

# The floor every reliant pod requests: 100m/128Mi, limited at 500m/512Mi.
# Stated, because forge's default (250m/1Gi, request = limit) is more than a
# laptop k3d node can schedule for every pod of a stack. An env raises it
# where it means to (control-plane's prod: lib/resources.k).
RESOURCES = fw.Resources {
    cpuRequestMillicores = forge.mcpu(100)
    memoryRequestBytes = forge.mib(128)
    cpuLimitMillicores = forge.mcpu(500)
    memoryLimitBytes = forge.mib(512)
}

# A percent of the container memory limit, as GOMEMLIMIT. forge derives 90
# for a forge.GoBuild workload, but a binding that pulls a prebuilt image or
# builds with a ShellBuild (control-plane's) hides that the binary is Go, and
# then the runtime never feels the limit: the worker's heap grew toward GOGC's
# target until the kernel OOM-killed it, twice on 2026-10-07. Declared, so it
# survives a binding that replaces `build`.
GO_MEMORY_LIMIT_PERCENT = 90

# forge's restricted-PSA baseline mounts the root filesystem read-only. The
# binaries write certs and logs under DATA_DIR (/data), and a tool populates
# ~/.config, so without these a pod dies at boot with "mkdir data: read-only
# file system".
SCRATCH_VOLUMES = [
    fw.Volume {name = "data", mountPath = "/data", source = fw.VolumeSource {emptyDir = {}}}
    fw.Volume {name = "config", mountPath = "/home/nonroot/.config", source = fw.VolumeSource {emptyDir = {}}}
]

# The platform PriorityClass. The class itself is a cluster object the
# platform declares once per cluster (control-plane's lib/priority.k), ranked
# above every tenant class, so a busy node cannot let a tenant's daemon pod
# preempt the API it is served by.
PRIORITY_CLASS = "reliant-platform"

# Every pod-only fact above, in the order a workload states them.
_pod = {
    probes = PROBES
    resources = RESOURCES
    volumes = SCRATCH_VOLUMES
    priorityClassName = PRIORITY_CLASS
    go_memory_limit_percent = GO_MEMORY_LIMIT_PERCENT
}

# ── The dev signer is dev's alone ───────────────────────────────────────────
# AUTH_DEV_JWT_SECRET makes the api-server accept HS256 tokens minted with it
# beside the Supabase key (internal/auth WithDevSigner): dev sign-in, and dev
# tier only. The process refuses to start with it outside that tier; this
# refuses to RENDER it there, wherever the api-server is bound — every env of
# this repo and of control-plane binds `api_server` below, so the check runs on
# the env each binding finally gives it. The tier is the one the process will
# read (RELIANT_ENV, else NODE_ENV), parsed as internal/runenv.Parse does:
# unset or unrecognised is prod. The web half, the VITE_DEV_SIGN_IN build flag
# on a frontend, is refused outside dev by dev_sign_in.k's only_in_dev.
DEV_SIGNER_ENV = "AUTH_DEV_JWT_SECRET"
_DEV_TIER_NAMES = ["dev", "development", "local", "e2e"]

# dev_signer_allowed reports whether env may carry the dev signer: it does not,
# or the tier it runs in is dev.
dev_signer_allowed = lambda env: {str:} -> bool {
    _tier = env.RELIANT_ENV if "RELIANT_ENV" in env else (env.NODE_ENV if "NODE_ENV" in env else "")
    DEV_SIGNER_ENV not in env or (typeof(_tier) == "str" and _tier.lower() in _DEV_TIER_NAMES)
}

# The api-server's workload: a forge Workload that refuses the dev signer
# outside the dev tier. A schema check, so it runs again on every binding's
# union (`api_server | {env = ...}`), on the final env.
schema APIServerWorkload(fw.Workload):
    check:
        dev_signer_allowed(env or {}), "${DEV_SIGNER_ENV} is a dev-only signer: ${name} sets it outside the dev tier " \
            "(its RELIANT_ENV is not dev, development, local or e2e; unset is prod). Only a dev environment may set it " \
            "(reliant deploy/kcl/workloads.k, control-plane#2735)."

# ── The workloads ───────────────────────────────────────────────────────────

# The schema migration, to completion, before every other workload. It takes
# the resource floor and nothing else: it serves no probe, and where a binding
# gates one workload with it (`before = [<name>]`) it becomes that pod's init
# container, which forge refuses a PriorityClass of its own.
migrate = fw.Workload {
    name = "reliant-migrate"
    kind = "job"
    build = _build
    args = ["db", "migrate", "up"]
    before = [fw.BEFORE_ALL]
    config_secrets = ["DATABASE_URL"]
    resources = RESOURCES
}

# release_dependencies: the contract each peer must keep for this workload to
# ship without it. A conservative closure requirement, NOT compatibility
# evidence: only a trusted exact-base/candidate validator may waive a peer.
# The contract names are shared with control-plane's release records; keep
# them.

# Every Connect service, on one mux (GRPC_PORT), plus /health on HEALTH_PORT.
api_server = APIServerWorkload {
    name = "reliant-api-server"
    kind = "service"
    build = _build
    release_dependencies = {
        "reliant-temporal-worker" = "reliant/api-worker-db-temporal/v1"
        "daemon-gateway" = "reliant/api-gateway-nats/v1"
    }
    ports = [fw.Port {name = "grpc", port = 9090, expose = True}]
    config_secrets = ["RELIANT_VAULT_KEY", "AUTH_API_KEY"]
    serves = [
        "account", "approval", "attachment", "background", "catalog", "chat",
        "connection", "connector", "daemon", "daemon-registry", "file-system",
        "forge", "inbox", "mcp", "message", "model-endpoint", "package-commands",
        "plan", "preset", "project", "provider-account", "question", "repo",
        "run", "scenario", "settings", "streaming", "system", "task", "terminal",
        "token", "tool-call", "trigger", "workflow", "worktree",
    ]
} | _pod | config_gen.root_of("api")

# The Temporal worker. Nothing dials it: its only listener is the health server.
temporal_worker = fw.Workload {
    name = "reliant-temporal-worker"
    kind = "worker"
    build = _build
    release_dependencies = {
        "reliant-api-server" = "reliant/api-worker-db-temporal/v1"
        "daemon-gateway" = "reliant/worker-gateway-nats/v1"
    }
    ports = [fw.Port {name = "health", port = HEALTH_PORT}]
    config_secrets = ["RELIANT_VAULT_KEY"]
} | _pod | config_gen.root_of("worker")

# The daemon gateway: the bidi stream every daemon dials (TOOLS_DAEMON_PORT).
daemon_gateway = fw.Workload {
    name = "daemon-gateway"
    kind = "service"
    build = _build
    release_dependencies = {
        "reliant-api-server" = "reliant/api-gateway-nats/v1"
        "reliant-temporal-worker" = "reliant/worker-gateway-nats/v1"
    }
    ports = [fw.Port {name = "grpc", port = 9190, expose = True}]
    serves = ["tools-daemon"]
} | _pod | config_gen.root_of("gateway")

ALL: [fw.Workload] = [migrate, api_server, temporal_worker, daemon_gateway]

# The GTM assets bucket's connection: forge.Bucket "gtm-assets", as the seven
# GTM_ASSETS_* leaves of GtmAssetsConfig (gtm_config.proto). The api-server and
# the worker read it (the worker runs the agent's attach_gtm_asset). It is NOT
# on their declarations above: control-plane binds those too, and every env
# that merges this must declare the bucket. An env merges it into those two
# workloads' env (dev/main.k does); one that does not has GTM assets off.
GTM_ASSETS_ENV = forge.bucket_env("GTM_ASSETS", "gtm-assets")

# ── What reliant builds and ships, but never runs ───────────────────────────
#
# Artifacts, not processes: each is `kind = "tool"` on `forge.BuildOnly`, so
# no env runs it and `ALL` above (what control-plane binds) does not name it.
# An env builds one only when it lists it.

# The production server image: the root Dockerfile, which compiles
# ./cmd/reliant in a glibc builder and packages it on debian (the one image
# every server role runs; the Dockerfile's header says why the builder's libc
# is load-bearing). The four server workloads above keep their GoBuild, which
# is what a host env runs; this is the image a cluster pulls.
#
# The Dockerfile's version stamp is five build args (VERSION, COMMIT,
# BUILD_DATE, BRANCH, COMMIT_TIME). Their values are per commit, so the env that
# binds this passes them (`server_image_build`), never this declaration. forge
# passes a DockerBuild only the build_args declared here, plus its own
# SOURCE_DATE_EPOCH and --provenance=false (reproducible digests); it stamps
# nothing itself.
#
# `pgo` is left at its default on purpose. The Dockerfile's `go build` takes
# Go's own -pgo=auto, which would read cmd/reliant/default.pgo if one were
# committed (none is), and it declares no FORGE_PGO_* ARG, so `pgo` here could
# only change what `forge pgo pull` reads. That has nowhere to read from: no
# reliant env runs the server under forge observability (prod runs it from
# control-plane), so the profiles live there.
#
# `inputs` lets a `forge env build <env> --release <v>` reuse an earlier
# release's image instead of running the docker build: forge fingerprints the
# files these globs select (relative to the project root) together with the
# whole declaration (build_args included), the Dockerfile's content, the
# platform and the destination repository, and when all of it equals what an
# earlier release recorded, and the registry still serves that digest, it
# tags that digest with the new release. The apt layer is not reproducible, so
# a rebuild of an unchanged tree would push a new digest and roll every
# Deployment pinned to the image; reuse is what keeps it the same bytes. A
# build that does not push, or `--rebuild`, always runs docker.
#
# They are what the image is a function of: `COPY . .` sends the whole
# context, but the only thing the runtime stage takes from it is the binary,
# which `go build ./cmd/reliant` compiles from go.mod/go.sum, the packages it
# imports (every one lives under cmd/, internal/, pkg/, gen/, db/ and sdk/go/,
# the Go SDK go.mod replaces) and the files they go:embed, which sit in those
# package directories. Test files are excluded, as .dockerignore excludes them
# from the context. A new top-level directory a package of the binary lives in
# must be added here, or a change to it would reuse a stale image.
#
# Reuse needs build args that are a pure function of the commit: a stamp that
# varies per build (a wall-clock BUILD_DATE) never matches, and one that
# carries the release tag would reuse an image stamped with the earlier tag.
# The release env derives every stamp from the commit (release/main.k).
server_image_inputs = [
    "Dockerfile"
    ".dockerignore"
    "go.mod"
    "go.sum"
    "cmd/**"
    "internal/**"
    "pkg/**"
    "gen/**"
    "db/**"
    "sdk/go/**"
    "!**/*_test.go"
]

server_image_build = lambda build_args: {str: str} -> forge.DockerBuild {
    forge.DockerBuild {
        dockerfile = "Dockerfile"
        build_args = build_args
        inputs = server_image_inputs
    }
}

server_image = fw.Workload {
    name = "reliant-image"
    kind = "tool"
    runtime = forge.BuildOnly {}
    build = server_image_build({})
}

# The Delve image: Dockerfile.debug, the server binary built without
# optimisations (-gcflags=all=-N -l) and run under a headless dlv
# (scripts/dlv-entrypoint.sh), for attaching a debugger to a reliant role in a
# cluster. A developer's tool, never a release artifact: no `image`, so it is
# a local image that nothing pushes. dev/main.k binds it only under
# `-D debug_image=true`, because `forge env up dev` builds every build-only
# workload it binds and dev needs no docker.
debug_image = fw.Workload {
    name = "reliant-debug-image"
    kind = "tool"
    runtime = forge.BuildOnly {}
    build = forge.DockerBuild {dockerfile = "Dockerfile.debug"}
}

# The standalone CLI: ./cmd/reliant cross-compiled for the platform matrix
# .goreleaser.yml declares — linux, darwin and windows on amd64 and arm64,
# minus windows/arm64 — one build variant each, named <goos>-<goarch> so forge
# writes bin/reliant-cli-<goos>-<goarch> (.exe on windows). The name matches
# control-plane's `reliant_cli_service`. forge builds every variant with
# CGO_ENABLED=0, as goreleaser's `env` does.
#
# release.yml's build-backend builds a sixth target, windows/arm64, with
# `-tags exp.winarm64` (a delve build tag). Adding it is one more entry with
# `tags = ["exp.winarm64"]`; goreleaser's matrix leaves it out, so this does too.
#
# The ldflags are goreleaser's: `-s -w` plus the internal/version stamp. The
# stamp's values are per build, so the env that binds this passes them
# (`reliant_cli_variants`, keyed like `server_image_build`'s build args), never
# this declaration; a key the stamp leaves out keeps its in-code "unknown".
# It also writes CommitTime, as release.yml's build does (goreleaser's
# ldflags predate that var).
# Each value must be free of spaces: forge joins the ldflags with one.
# goreleaser's four `internal/builddefaults` -X flags are not reproduced: that
# package no longer exists, so the linker ignores them, and with goreleaser's
# empty defaults they stamped nothing anyway. The OSS-clean equivalent is no
# endpoint stamp, which is what this builds (release.yml's
# pkg/config.BuildDefaults stamp is the release's opt-in, not goreleaser's).
_CLI_PLATFORMS = [
    ["linux", "amd64"]
    ["linux", "arm64"]
    ["darwin", "amd64"]
    ["darwin", "arm64"]
    ["windows", "amd64"]
]

# The internal/version var each stamp key writes (the Dockerfile's build-arg
# names, so one stamp serves both builds).
_VERSION_VARS = {
    VERSION = "Version"
    COMMIT = "Commit"
    BUILD_DATE = "Date"
    BRANCH = "Branch"
    COMMIT_TIME = "CommitTime"
}

_pad2 = lambda n: int -> str {
    ("0" + str(n))[-2:]
}

# The RFC 3339 UTC time of a commit time in unix seconds (the COMMIT_TIME
# stamp, scripts/release-identity.sh --commit-time), the BUILD_DATE a build
# stamps so that every one of its stamps is a function of the commit. "unknown"
# (the Dockerfile's default) for 0 or anything that is not a count of seconds.
# KCL has no epoch conversion, so this is Howard Hinnant's civil_from_days.
commit_date = lambda commit_time: str -> str {
    _s = int(commit_time) if commit_time.isdigit() else 0
    _z = _s // 86400 + 719468
    _era = _z // 146097
    _doe = _z - _era * 146097
    _yoe = (_doe - _doe // 1460 + _doe // 36524 - _doe // 146096) // 365
    _doy = _doe - (365 * _yoe + _yoe // 4 - _yoe // 100)
    _mp = (5 * _doy + 2) // 153
    _day = _doy - (153 * _mp + 2) // 5 + 1
    _month = _mp + 3 if _mp < 10 else _mp - 9
    _year = _yoe + _era * 400 + (1 if _month <= 2 else 0)
    _t = _s % 86400
    "unknown" if _s == 0 else "${_year}-${_pad2(_month)}-${_pad2(_day)}T${_pad2(_t // 3600)}:${_pad2(_t % 3600 // 60)}:${_pad2(_t % 60)}Z"
}

reliant_cli_variants = lambda stamp: {str: str} -> [forge.BuildVariant] {
    _ldflags = ["-s", "-w"] + [
        "-X github.com/reliant-labs/reliant/internal/version.${var}=${stamp[key]}"
        for key, var in _VERSION_VARS if key in stamp
    ]
    [forge.BuildVariant {
        name = "${p[0]}-${p[1]}"
        goos = p[0]
        goarch = p[1]
        ldflags = _ldflags
    } for p in _CLI_PLATFORMS]
}

reliant_cli = fw.Workload {
    name = "reliant-cli"
    kind = "tool"
    runtime = forge.BuildOnly {build_variants = reliant_cli_variants({})}
    # -trimpath reaches each BuildOnly variant from here (forge #2690), as the
    # Dockerfile's build passes it.
    build = forge.GoBuild {cmd = "./cmd/reliant", flags = ["-trimpath"]}
}

# ── The desktop app ─────────────────────────────────────────────────────────
#
# The packaged Electron app users download: .dmg/.zip on macOS, .exe on
# Windows, .AppImage/.deb on Linux. The build is electron/scripts/pack-desktop.js,
# the one electron-builder call release.yml's release-macos, release-linux and
# release-windows jobs also run, here for the machine's own platform (`host`)
# and never published (`never`). The signed, published installers are still
# release.yml's: it holds the signing and R2 secrets this build never sees.
#
# PREREQUISITES. This packages; it does not build what it packages, because
# release.yml downloads those from other jobs. pack-desktop.js refuses, naming
# what is missing, unless they are on disk (paths from the repo root):
#
#   electron/node_modules                    cd electron && pnpm install --frozen-lockfile
#   electron/resources/server/<plat>-<arch>/ scripts/build-electron.sh <mac|linux|win>
#     reliant-backend[.exe], both arches
#   frontends/web/dist                       cd frontends/web && pnpm install --frozen-lockfile \
#                                              && NODE_ENV=production pnpm run build
#   electron/src/build-config.js and         cd electron && node scripts/with-release-config.mjs
#     electron/src/release-renderer-config.js  pnpm run build:config
#
# No `image`: the app is not one, and at this forge pin a build-only ShellBuild
# needs none (`forge env build release --target reliant-desktop` runs it), so
# this does not invent one the way control-plane's reliant-cli declaration
# does. forge still writes a placeholder `reliant-desktop:latest` into
# .forge/state after the build; that is a forge defect (reported), not an
# artifact.
#
# No `inputs`: they would let forge skip the command when they are unchanged,
# and with nothing recording what the last run produced, a skip would report a
# build whose electron/dist may since have been deleted.
#
# FOLLOW-UP (forge #2428, open): once `Build.outputs` lands, declare the files
# this writes so a release records them — electron/dist/ *.dmg, *.zip,
# *-mac.yml, *.exe, latest.yml, alpha.yml, *.AppImage, *.deb, *-linux.yml,
# *-linux-arm64.yml and *.blockmap (release.yml's upload-artifact globs).
#
# `cwd` resolves against the project that renders this. control-plane imports
# this file but binds only the server workloads, so it is always reliant's.
desktop_app = fw.Workload {
    name = "reliant-desktop"
    kind = "tool"
    runtime = forge.BuildOnly {}
    build = forge.ShellBuild {
        cwd = "electron"
        cmd = "node scripts/pack-desktop.js host electron-builder.js never"
    }
}
