"""
Forge KCL — shared base helpers.

Common env-var groups and init-container patterns that most forge
projects layer onto their typed entities. Pure data — no rendering
logic. The render layer (`render.k`) is what walks Bundle and
projects to JSON / k8s manifests.

Schemas like `EnvVar` are declared in `schema.k`. Sibling files in
the same KCL package see them as bare names — no `import` needed.
"""

import json
import regex
import .lib.images as img_lib

# ---------------------------------------------------------------------------
# Local-vs-cloud target detection.
# ---------------------------------------------------------------------------
#
# The Bundle build-platform guard needs to tell a LOCAL deploy target (k3d on
# the dev host — host arch, an in-host registry) apart from a CLOUD target
# (GKE/EKS/… — a fixed node arch, a real remote registry). It classifies by the
# registry host in the WORKLOAD's image, because that is the only place a
# registry is declared, so the predicate lives ONCE here.
#
# The registry is the load-bearing signal: a localhost-style registry can
# only be reached from the host's own k3d cluster — a CLOUD node could never
# pull from it — so `localhost:`, `127.0.0.1:`, and `*.localhost:` registries
# unambiguously mark a LOCAL target, while any real remote registry
# (ghcr.io/…, *.pkg.dev/…, *.amazonaws.com/…, a bare `org/img`) marks a CLOUD
# one. This is exactly the dev/e2e (localhost:5051) vs staging/prod
# (ghcr.io/…) split the control-plane envs already encode, so the guards stay
# inert for the local envs that legitimately omit a platform / ride a local
# registry, and fire only for the cloud envs where the omission is a bug.
#
# image_registry_host(image) — the registry HOST an image reference names
# (`ghcr.io`, `localhost:5051`), or "" when it names none. Re-exported from
# lib/images.k so an env's main.k can classify its own images with the same
# rule forge's guards use, rather than re-deriving Docker's heuristic.
image_registry_host = img_lib.registry_host

# image_repository(image) — an image reference minus any `:tag` / `@digest`,
# registry host INCLUDED. The build-state and release-ledger key, re-exported
# for a main.k that needs to name the same repository forge does.
image_repository = img_lib.repository

# is_local_registry(registry) — True when `registry` is a host-local registry
# (the LOCAL-target signal). Matches a leading `localhost`, `127.0.0.1`, or
# any `*.localhost` host (with or without a `:port`), before the first `/`.
is_local_registry = lambda registry: str -> bool {
    _host = registry.split("/")[0]
    bool(regex.match(_host, r"^(localhost|127\.0\.0\.1|[A-Za-z0-9.-]+\.localhost)(:[0-9]+)?$"))
}

# builds_an_image(w) — True when a workload's `build` PRODUCES (and pushes)
# a container image whose ARCH the build pipeline must pin from the env's
# `platform`. That is a DockerBuild (forge `docker build`s it) or a real
# ShellBuild — an external `sh -c` build that cross-compiles + `docker build`s
# + pushes. A no-op ShellBuild (`cmd = "true"`, the idiomatic "suppress the
# default Go build for a prebuilt sibling image" hatch) pushes NOTHING, so it
# imposes no arch requirement and does NOT count. A GoBuild produces a binary
# COPY'd into the project image whose arch forge already derives — also not an
# external image build. This is the signal the `platform`-required guard keys
# on: an env that ships a real external image build onto a CLOUD node MUST
# state the node arch, or the build silently falls through to the dev host's
# arch (the arm64-image-on-amd64-node class that ImagePull/exec-format-errors
# in prod).
builds_an_image = lambda w: Workload -> bool {
    _b = w.build
    True if _b and _b.type == "docker" else \
    (True if _b and _b.type == "shell" and _b.cmd != "true" else False)
}

# Default env-var bundle for accessing a Postgres database via
# a Kubernetes Secret. Projects that use a different secret name
# can copy this and tweak it.
DB_ENV: [EnvVar] = [
    EnvVar {
        name = "DATABASE_URL"
        secret_ref = "db-credentials"
        secret_key = "database-url"
    }
    EnvVar {
        name = "DB_HOST"
        secret_ref = "db-credentials"
        secret_key = "host"
    }
    EnvVar {
        name = "DB_PORT"
        secret_ref = "db-credentials"
        secret_key = "port"
    }
    EnvVar {
        name = "DB_NAME"
        secret_ref = "db-credentials"
        secret_key = "name"
    }
    EnvVar {
        name = "DB_USER"
        secret_ref = "db-credentials"
        secret_key = "username"
    }
    EnvVar {
        name = "DB_PASSWORD"
        secret_ref = "db-credentials"
        secret_key = "password"
    }
]

# The OpenTelemetry collector forge renders for the node agent and the local
# forwarder: contrib, pinned by digest. >= 0.162 is the floor that lets the
# profiles signal flow (service.profilesSupport). The compose forwarder in
# docker-compose.yml pins the same reference (a test keeps them equal). From
# the OpenTelemetry project's own ghcr (the same digest Docker Hub serves),
# because a node pulling from Docker Hub anonymously shares its IP's limit.
OTEL_COLLECTOR_IMAGE: str = "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.162.0@sha256:39923a8e431bd1f57be82411999d389fcfe40857492e4365456d97a4c1f74be6"

# The node-local Service the agent DaemonSet is reached through
# (internalTrafficPolicy: Local), in `Observability.agent.namespace`.
OTEL_AGENT_SERVICE: str = "otel-agent"

# forge.ControlPlaneSecret: what the declared control plane writes under each
# kind, and where a workload reads it. ONE contract, spelled once: the control
# plane (which writes the Secret), the node agent (pkg/deploy, `token_secret`)
# and pkg/observe (`OTEL_EXPORTER_OTLP_HEADERS_FILE`) all depend on it.
#
# telemetry_token writes two keys of one Secret:
#   OTEL_EXPORTER_OTLP_HEADERS  `authorization=Bearer <jwt>`, the OTEL headers
#                               format, which a workload reads from a file;
#   token                       the bare `<jwt>`, which the node agent's
#                               bearertokenauth extension reads from a file.
#
# The identity kinds are DERIVED from Bundle.identity and never declared by
# hand. `identity`'s keys depend on the applications, so the map holds none and
# ControlPlaneSecret.keys carries them; the other two have fixed keys.
CONTROL_PLANE_SECRET_KINDS: {str: [str]} = {
    "telemetry_token" = ["OTEL_EXPORTER_OTLP_HEADERS", "token"]
    "identity" = []
    "identity_machine_key" = ["key.json"]
    "identity_jwks" = ["jwks.json"]
}

# The kinds forge.ManagedIdentity derives. Declaring one in
# Bundle.control_plane_secrets is refused: the declaration is the identity.
IDENTITY_SECRET_KINDS: [str] = ["identity", "identity_machine_key", "identity_jwks"]

# ---------------------------------------------------------------------------
# forge.ManagedIdentity: the names forge and the control plane agree on.
# ---------------------------------------------------------------------------

# The Kubernetes Secret a cluster consumer reads an identity's outputs from is
# `forge-identity-<name>` (+ `-jwks` for the signing-key snapshot); a HOSTED
# consumer reads managed secrets named `RELIANT_IDENTITY_<NAME>_<KEY>`. Both
# prefixes are reserved: only the control plane writes them, so forge never
# asks a person to set one.
IDENTITY_SECRET_PREFIX: str = "forge-identity-"
IDENTITY_MANAGED_PREFIX: str = "RELIANT_IDENTITY_"
IDENTITY_DOMAIN: str = "reliantidentity.com"

# Where a cluster consumer finds the files it re-reads (the broker token and the
# JWKS snapshot): whole-directory Secret volumes, so a rotation reaches the pod
# without a restart.
IDENTITY_DIR: str = "/var/run/secrets/forge/identity"
IDENTITY_JWKS_DIR: str = "/var/run/secrets/forge/identity-jwks"

# What an identity can hand a workload. The APPLICATION outputs below belong
# to the application a consumer brokers for; the rest are the instance's.
IDENTITY_OUTPUTS: [str] = [
    "audience", "broker_token", "client_id", "idp_base", "issuer",
    "jwks", "login_uri", "redirect_uri", "roles_claim", "scopes",
]
# The outputs a consumer may decline to have projected (IdentityConsumer.omit).
# Never the ones that DEFINE the identity: without issuer and audience a
# validator has nothing to pin tokens to, and a native broker cannot sign in
# without its token, client id, redirect URI and base. A consumer that does not
# want those projected is a deliver_only consumer.
IDENTITY_OMITTABLE_OUTPUTS: [str] = ["jwks", "login_uri", "roles_claim", "scopes"]
IDENTITY_APP_OUTPUTS: [str] = ["broker_token", "client_id", "login_uri", "redirect_uri"]

# The typed-config field each output fills, unless a consumer remaps it
# (IdentityConsumer.fields). The env var is the upper-cased field name, the
# convention every forge config field follows. The broker token and the JWKS
# snapshot also have `<field>_file` siblings, which a cluster consumer uses
# instead (a file is re-read; an env var is not).
IDENTITY_FIELDS: {str: str} = {
    "audience" = "jwt_audience"
    "broker_token" = "idp_broker_token"
    "client_id" = "oidc_client_id"
    "idp_base" = "idp_base"
    "issuer" = "jwt_issuer"
    "jwks" = "jwt_jwks_snapshot"
    "login_uri" = "idp_login_uri"
    "redirect_uri" = "oidc_redirect_uri"
    "roles_claim" = "jwt_roles_claim"
    "scopes" = "oidc_scopes"
}

# The outputs that, when a hand-written literal already fills their field, are a
# CONFLICT. The others (scopes, roles_claim, login_uri) have non-empty proto
# defaults, so a literal there is the default and the identity replaces it.
IDENTITY_CONFLICT_OUTPUTS: [str] = [
    "audience", "client_id", "idp_base", "issuer", "redirect_uri",
]

# The Secret the node agent reads its ingest token from when the destination is
# the control plane's (ObservabilityDestination.token_from_control_plane): one
# per cluster, in Observability.agent.namespace. The agent's bearertokenauth
# reads the `token` key (pkg/deploy mounts it at /etc/otel-auth/token).
OTEL_AGENT_TOKEN_SECRET: str = "forge-otlp-token"

# The pod volume, its directory and the file a workload reads the headers from
# when its OTLP auth is a ControlPlaneSecret. A whole-directory Secret volume,
# never subPath: the kubelet refreshes a directory in place, so a renewal needs
# no restart. The volume name is fixed because a (cluster, namespace) has at
# most one telemetry_token ControlPlaneSecret.
CONTROL_PLANE_OTLP_VOLUME: str = "forge-otlp-auth"
CONTROL_PLANE_OTLP_DIR: str = "/var/run/secrets/forge/otlp"
CONTROL_PLANE_OTLP_FILE: str = "headers"

# OTLP exporter env vars for a workload on a cluster that runs the node agent
# `Bundle.observability` renders. A project that declares Bundle.observability
# needs none of this: forge derives the same pair. This is the splice default
# for one that wires its workloads by hand.
#
# 4318 is OTLP/HTTP, the runtime's default protocol. Sampling is declared on
# Observability.sampling and applied by the agent, so no sampler is set here.
OTEL_ENV: [EnvVar] = [
    EnvVar {
        name = "OTEL_EXPORTER_OTLP_ENDPOINT"
        value = "http://${OTEL_AGENT_SERVICE}.forge-observability.svc.cluster.local:4318"
    }
    EnvVar {
        name = "OTEL_EXPORTER_OTLP_PROTOCOL"
        value = "http/protobuf"
    }
]

# Projects override this in their own files when they need a base
# layer for every entity.
COMMON_ENV: [EnvVar] = []

# env_merge(base, overrides) — combine two EnvVar lists with name-keyed,
# OVERRIDE-WINS semantics. This is the explicit, public replacement for the
# `base + overrides` KCL list-concatenation footgun.
#
# Why this exists (the footgun it kills)
# ======================================
# KCL list `+` CONCATENATES — it does NOT de-duplicate. So
# `cfg.APP_ENV + [EnvVar { name = "LOG_LEVEL", value = "debug" }]` where
# APP_ENV already carries a LOG_LEVEL emits the variable TWICE, and
# kubectl server-side apply REJECTS the Deployment ("duplicate entries for
# key [name=...]"). Today that is silently rescued by a HIDDEN
# `_dedup_env_vars` the render layer applies downstream — an implicit
# contract an author writing a new env can't see. `env_merge` makes the
# "later wins on name conflict" rule VISIBLE on the public `forge` surface,
# so a reader (human or LLM) can author a correct override without knowing
# the renderer's secret.
#
# Semantics (matches the render-time dedup exactly, so it is
# render-equivalent to `base + overrides`):
#   * one entry per `name`;
#   * on a name conflict the LATER occurrence wins — an `overrides` entry
#     supersedes a `base` entry, exactly the way the host-mode env layering
#     and the K8sCluster render-time dedup already resolve it;
#   * surviving entries keep their LAST-occurrence position (the same order
#     the renderer's dedup produced), so the rendered container `env` block
#     is byte-identical before/after migrating a `+` chain to `env_merge`.
#
# It is variadic-by-nesting: chain `env_merge(env_merge(a, b), c)` to layer
# more than two sources, left-to-right, last-wins — the direct analogue of
# `a + b + c` but de-duplicated and explicit.
#
# Example:
#   _env = forge.env_merge(cfg.APP_ENV, [
#       forge.EnvVar { name = "LOG_LEVEL", value = "debug" }  # wins over APP_ENV's
#   ])
env_merge = lambda base: [EnvVar], overrides: [EnvVar] -> [EnvVar] {
    _all = base + overrides
    [
        _all[i] for i in range(len(_all))
        # keep this entry only if no LATER entry shares its name (last-wins)
        if len([1 for j in range(i + 1, len(_all)) if _all[j].name == _all[i].name]) == 0
    ]
}

# ---------------------------------------------------------------------------
# Standard `-D <key>=<value>` render options.
# ---------------------------------------------------------------------------
#
# Every render the forge CLI drives passes a STANDARD set of top-level KCL
# bindings via `kcl run -D <key>=<value>` (see
# internal/cluster.RenderManifests / renderDArgs). They are how the deploy /
# build / dev-cluster-reload pass per-invocation facts INTO your main.k. KCL
# surfaces them through the `option("<key>")` builtin; the typed accessors
# below wrap each one with a default + doc so a fresh reader can DISCOVER the
# whole set from the `forge` surface instead of copying a sibling env.
#
# The standard keys, ALWAYS passed:
#
#   env          -> the environment name ("dev" / "staging" / "prod" / …).
#                   forge.env(). Use for per-env conditional manifests
#                   (see forge.env() / README "Per-env conditional manifests").
#   image_tag    -> the resolved image tag for the env. forge.image_tag(env)
#                   (declared below; precedence: -D override > per-env
#                   default > "latest").
#   namespace    -> the k8s namespace the manifests deploy into.
#                   forge.namespace(default).
#
# Passed CONDITIONALLY (only when forge has a value to bind):
#
#   image_digests -> JSON name→digest map `forge env deploy` pins so each image
#                   resolves to ITS content-addressed digest. forge.image_digests().
#                   Absent on the local-registry / `kcl run` path (→ {}).
#   frontend_outputs -> JSON name→build-shape map ("static" | "standalone" |
#                   "server") read from each frontend's next.config.
#                   forge.render refuses a server build on a static runtime
#                   with it. Absent on a plain `kcl run` (→ nothing judged).
#
# The image REGISTRY is not among them, and is not an env field either: it is
# part of each WORKLOAD's `image`. forge binds no option for it and invents no
# value for it — it appends the tag and digest above to the reference the
# author wrote. forge.image_registry_host reads the host off that reference;
# an env that needs a DIFFERENT image says so, by setting `image` on the
# workload in its own KCL.
#
# Per-env app config is NOT passed via `-D`: it lives in the typed AppConfig
# instance in `deploy/kcl/<env>/config.k` and is projected into each workload's
# env by `config_gen.appConfigEnvMap` — read config there, not via raw
# `option()`.

# env() — the current environment name (the `-D env=<name>` binding the forge
# CLI passes on every render). `default` is used for a plain `kcl run` with no
# binding. Use for per-env conditional manifests:
#
#     _is_dev_host = forge.env() == "dev-host"
#     manifests = [] if _is_dev_host else [forge.Manifests {objects = [...in-cluster infra...]}]
env = lambda default: str = "dev" -> str {
    option("env") or default
}

# namespace(default) — the k8s namespace the render deploys into (the
# `-D namespace=<ns>` binding the forge CLI passes on every cluster render).
# `default` is the per-env literal your main.k falls back to for a plain
# `kcl run`.
namespace = lambda default: str -> str {
    option("namespace") or default
}

# default_kubeconfig() — the path to the DEFAULT kubeconfig on the machine
# doing the render (the `-D kubeconfig=<path>` binding the forge CLI passes on
# every render).
#
# This is the file `k3d cluster create` + `k3d kubeconfig merge
# --kubeconfig-merge-default` write a cluster's entry into, which is what makes
# it the right answer for "where does this cluster's kubeconfig live": forge
# resolves `$KUBECONFIG`'s first entry when set, else `~/.kube/config`, the
# same precedence kubectl itself uses.
#
# It is not meant to be called directly in an env's main.k — reference
# `<cluster>.kubeconfig` instead, which is this value attached to the cluster
# whose context you actually want. The accessor exists so the schema's own
# default has something to read.
#
# "" for a plain `kcl run` with no binding, so a module still renders outside
# forge. See Cluster.kubeconfig for why that is not filled in with a guess.
default_kubeconfig = lambda -> str {
    option("kubeconfig") or ""
}

# Per-env default image tags. Kept centrally so the forge CLI's
# `-D image_tag=...` override has ONE fallback table to consult; the
# scaffold templates no longer re-declare a per-env literal.
_IMAGE_TAG_DEFAULTS: {str: str} = {
    "dev" = "latest"
    "staging" = "staging"
    "prod" = "stable"
}

# image_tag(env) — resolve the image tag for an environment.
#
# Precedence:
#   1. `kcl run -D image_tag=<tag>` (the forge CLI passes this on every
#      deploy / build / dev cluster reload — see internal/cluster.RenderManifests)
#   2. The per-env default from _IMAGE_TAG_DEFAULTS (latest/staging/stable)
#   3. "latest" for unknown envs (so user-defined envs like "preview"
#      still get a usable tag without a forced override)
#
# Usage in a per-env main.k:
#
#     bundle = forge.Bundle {image_tag = forge.image_tag("dev"), ...}
#
# (Bundle.image_tag already defaults to exactly this; set it only to pin.)
image_tag = lambda env: str -> str {
    option("image_tag") or _IMAGE_TAG_DEFAULTS[env] or "latest"
}

# image_digests() — the per-image content-addressed digest map forge env deploy
# passes on the command line.
#
# forge env deploy resolves each built image's pushed manifest digest from the
# build state and passes the map as a JSON string:
#   `kcl run -D image_digests='{"reliant":"sha256:...","control-plane":"sha256:..."}'`
# keyed on the BARE image name. `forge.render` reads it to pin each cluster
# workload's image to ITS image's digest (lib/images.k); a main.k consults
# it for any hand-composed image reference (e.g. a DAEMON_IMAGE env var that
# names workspace-base directly).
#
# Empty / unset (the local-registry e2e path and a plain `kcl run` with no
# digest binding) returns `{}`, so every image stays on its tag — unchanged.
# Complete per-workload/slot references. This map is never flattened by repo.
workload_image_digests = lambda -> {str: {str: str}} {
    _raw = option("forge_workload_image_digests") or ""
    json.decode(_raw) if _raw else {}
}

# Explicit helper for main, sidecar, init, pre-pull and build-only consumers.
# Immutable composition renders refuse ANY missing/mutable reference.
workload_image_ref_for = lambda workload: str, slot: str, image: str, tag: str -> str {
    _pins = workload_image_digests()
    _slots = _pins[workload] if workload in _pins else {}
    _ref = _slots[slot] if slot in _slots else ""
    _immutable = option("forge_immutable_release") or False
    assert not _immutable or (_ref and regex.match(_ref, r"^.+@sha256:[a-f0-9]{64}$")), "immutable release requires workload/slot pin ${workload}/${slot}"
    _ref if _ref else img_lib.image_ref(image, tag, image_digests())
}

image_digests = lambda -> {str:str} {
    _raw = option("image_digests") or ""
    json.decode(_raw) if _raw else {}
}

# image_ref_for(image, tag) — build an image reference that prefers the
# per-image digest from image_digests() when one was captured, else falls back
# to `<image>:<tag>`. The companion to forge.image_digests() for hand-composed
# references that are not a workload's own image — chiefly an env var that
# names an image by hand (DAEMON_IMAGE = the workspace-base ref a controller
# reads at runtime).
#
# `image` is the FULL reference, registry host included, exactly as a workload
# declares it — there is no registry parameter, because there is no env-level
# registry to pass. `tag` is the tag the caller means, so both digest keys are
# consulted, most specific first, matching lib/images.k's image_ref: a digest
# captured for THIS `image:tag` wins, then the untagged repository's. The
# emitted reference keeps the tag beside the digest
# (`<image>:<tag>@sha256:…`) when the digest is tag-specific, so the two
# helpers produce the same text for the same image and a reader can still see
# which tag was meant.
image_ref_for = lambda image: str, tag: str -> str {
    _d = image_digests()
    _tagged = "${image}:${tag}"
    _tag_digest = _d[_tagged] if (_tagged in _d) else ""
    _bare_digest = _d[image] if (image in _d) else ""
    "${image}:${tag}@${_tag_digest}" if _tag_digest \
        else ("${image}@${_bare_digest}" if _bare_digest \
        else "${image}:${tag}")
}

# ---------------------------------------------------------------------------
# IPv4 CIDR predicates — used by Cluster's cluster_cidr / service_cidr checks.
# ---------------------------------------------------------------------------
#
# These exist so a mistyped or overlapping CIDR fails at KCL LOAD time rather
# than as an unroutable cluster hours later. k3s accepts both mistakes: a
# malformed `--cluster-cidr` is rejected only when the server starts (inside a
# container, in a log nobody is tailing), and a cluster_cidr that CONTAINS its
# own service_cidr is accepted outright — the cluster then comes up and
# service IPs collide with pod IPs, which surfaces as intermittently
# unreachable Services, not as a configuration error.
#
# The helpers live in base.k (not inline in the check block) because a `check:`
# expression cannot declare intermediate names, and the overlap test needs
# several.

# is_ipv4_cidr(c) — True when `c` is a well-formed IPv4 CIDR: four octets in
# 0-255, and a prefix length in 0-32. The regex alone is not sufficient —
# it happily matches "300.0.0.0/33" — so the bounds are checked too.
is_ipv4_cidr = lambda c: str -> bool {
    bool(regex.match(c, r"^[0-9]{1,3}(\.[0-9]{1,3}){3}/[0-9]{1,2}$")) and \
    all o in [int(p) for p in c.split("/")[0].split(".")] { o <= 255 } and \
    int(c.split("/")[1]) <= 32
}

# _ipv4_to_int(ip) — dotted-quad to its 32-bit integer value.
_ipv4_to_int = lambda ip: str -> int {
    _o = [int(p) for p in ip.split(".")]
    _o[0] * 16777216 + _o[1] * 65536 + _o[2] * 256 + _o[3]
}

# cidrs_overlap(a, b) — True when the two CIDR blocks share any address.
#
# Two blocks overlap exactly when one contains the other, which is true when
# their addresses agree on the SHORTER of the two prefixes. Comparing at the
# shorter prefix is what makes this correct for unequal prefix lengths:
# 10.16.0.0/14 and 10.20.0.0/20 are disjoint (prod's real GKE layout), while
# 10.42.0.0/16 and 10.42.128.0/20 are not.
#
# Both arguments must already satisfy is_ipv4_cidr — the caller's check block
# validates shape first, and KCL evaluates check conditions in order.
cidrs_overlap = lambda a: str, b: str -> bool {
    _pa = int(a.split("/")[1])
    _pb = int(b.split("/")[1])
    _p = _pa if _pa < _pb else _pb
    _shift = 32 - _p
    (_ipv4_to_int(a.split("/")[0]) >> _shift) == (_ipv4_to_int(b.split("/")[0]) >> _shift)
}
