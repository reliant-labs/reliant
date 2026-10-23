"""
Forge KCL — the lowering: `output = forge.render(bundle)`.

`render` is the ONE entrypoint (ADR 0002). A project's env main.k ends with
`output = forge.render(bundle)`; there is no other public top-level var, and
in particular no top-level `manifests` a bare `kcl run -S manifests |
kubectl apply` could consume: the cluster-bound workloads in the applyable
stream are forge.dev Workload RECORDS that forge expands in Go, so an apply
that bypasses forge could never be correct.

THE OUTPUT (internal/cli/testdata/render_contract/ pins it per runtime):

    project, env, image_tag
    lifecycle                 WHO APPLIES this env: "local" | "ephemeral",
                              or null for a real env reconciled from a
                              bundle. Never a refusal here — see Bundle
    applier                   "forge" for a real env forge applies by
                              declaration, until its units move (transitional),
                              else null
    hub_converges             the delivery units the hub converges, present only
                              when declared (a list, possibly empty)
    clusters, cluster_target, kubeconfig_secrets, connected_clusters
    network_policy            the env NetworkPolicy bundle's inputs, or null
    statefulset_resize        {image, disabled} tuning of the StatefulSet
                              volume-resize Job, present only when declared
    workloads[]               EVERY workload, of every runtime:
        {name, kind, image, build, runtime, spec}
          image   — the REPOSITORY forge builds it into, registry host
                    included ("" when it builds none): the build-state /
                    release-ledger key, and where a push goes
          build   — the typed build, or null (forge builds nothing)
          runtime — {type: host|compose|cluster|hosted|build-only, ...}
          spec    — v1alpha1.WorkloadSpec JSON (the generated
                    tiers.workload_json), identical to the record's spec
    infra[], frontends[], databases[]
    config_maps, gateways, http_routes, grpc_routes, runtime_classes,
    helm_charts, required_secrets, required_dns, secret_provider,
    rendered_secrets, control_plane
    manifests[]               the applyable stream: RuntimeClasses,
                              Namespaces, ConfigMaps, one `forge.dev/v1alpha1
                              Workload` record per CLUSTER-bound workload
                              (a tool is never scheduled, so has none),
                              ManagedDatabase records, Gateway API objects,
                              then Bundle.manifests (forge.Manifests)

Missing optional fields in the runtime/build/infra/frontend blocks project as
`null`, not omitted, so the consumer sees stable keys.

WHAT THE LOWERING DECIDES (per workload, `_lower`):

  * the RUNTIME: the workload's own. There is no env-level default: an env
    is a list of per-workload bindings (ADR 0002 §2);
  * the IMAGE: a cluster workload's image is resolved here, against the env's
    tag and `-D image_digests`, so the record names exactly what the cluster
    pulls. The REGISTRY comes from the image itself — the author declares the
    full reference and forge appends only the tag/digest; an image naming no
    registry host is refused here, naming the workload. A hosted workload carries the
    artifact name (forge's hosted publish pins it to repo@digest) or the
    author's third-party image; host / compose / build-only run no image;
  * ENV: the map becomes the wire's list, with each reference in its
    channel; a forge.WorkloadURL is RESOLVED to a value for a host or
    cluster referrer and KEPT as a reference for a hosted one. Keys the
    author left unset are DERIVED (`_derived_env`): OTEL_SERVICE_NAME (the
    workload's name), OTEL_RESOURCE_ATTRIBUTES (env, project, release) and,
    for a Go process in a container with a memory limit, GOMEMLIMIT;
  * PROBES: `_default_probes`, ADR 0002 §5;
  * the RUNTIME MASK: `_mask_violations` refuses, naming the workload, the
    field and the reason, what the bound runtime cannot honour.

The Kubernetes objects a cluster workload becomes (Deployment, Service,
Jobs, RBAC, PDB, NetworkPolicy, ...) are NOT rendered here: that is
pkg/deploy.RenderWorkloads, the one renderer, which the CLI and the control
plane both run.

This file is a SIBLING of schema.k and workload.k so it references Bundle,
Workload etc. as bare names; `import .schema` would fork type identity.
"""

import json
import regex
import .tiers
import .lib.gateway as gw_lib
import .lib.jwks as jwks_lib
import .lib.labels as labels_lib
import .lib.workload_url as wurl_lib
import .lib.images as img_lib
import .lib.capabilities as caps
import .lib.crd as crd_lib
import .lib.build as build_lib
import .lib.flux as flux_lib
import .lib.eso as eso_lib
import .lib.openbao as openbao_lib
import .lib.external_secret as es_lib

# Bare aliases for schema instantiation inside lambda bodies (KCL v0.11).
_TWorkload = tiers.Workload
_TEnvVar = tiers.EnvVar
_TProbes = tiers.Probes
_TSecretKeyRef = tiers.SecretKeyRef
_TConfigMapKeyRef = tiers.ConfigMapKeyRef
_TFieldRef = tiers.FieldRef
_TManagedSecretRef = tiers.ManagedSecretRef
_TDatabaseRef = tiers.DatabaseRef
_TWorkloadURLRef = tiers.WorkloadURLRef
_TResources = tiers.Resources
_TManagedIdSecret = ManagedSecret
_TIdSecretRef = SecretRef

# ---------------------------------------------------------------------------
# Runtime resolution.
# ---------------------------------------------------------------------------

# A workload's runtime: its own. Undefined when unbound (Bundle's check
# names the workload).
_bound_runtime = lambda w: any -> any {
    w.runtime if w.runtime else Undefined
}

# A database runs on a cluster or on the control plane: its own runtime,
# else (Undefined) the env's primary cluster target.
_db_runtime = lambda db: any -> any {
    db.runtime if db.runtime else Undefined
}

# The names that occur more than once, sorted.
_duplicates = lambda names: [str] -> [str] {
    sorted([n for n in {x: 0 for x in names} if len([y for y in names if y == n]) > 1])
}

# Every name the env declares: the WorkloadURL namespace and `--target` set.
_env_names = lambda workloads: [any], infra: [any], frontends: [any] -> [str] {
    [w.name for w in workloads] + [i.name for i in infra] + [f.name for f in frontends]
}

# Everything that is published to the control plane.
_hosted_names = lambda workloads: [any], databases: [any], frontends: [any] -> [str] {
    [w.name for w in workloads if w.runtime and w.runtime.type == "hosted"] \
        + [db.name for db in databases if db.runtime and db.runtime.type == "hosted"] \
        + [f.name for f in frontends if f.runtime and f.runtime.type == "hosted"]
}

# A cloud-bound workload whose build produces an image, on a target that
# states no platform (see the Bundle check).
#
# The local-vs-cloud signal is the IMAGE's registry host, because that is
# where a registry is declared. A `localhost:`/`*.localhost` host can only be
# served to the dev host's own k3d cluster, so its nodes share the dev arch and
# an omitted platform correctly tracks it; any real remote host means nodes of
# a fixed, possibly different arch.
# A hostless image is NOT treated as cloud here: it is already a hard render
# error naming the workload (_check_image_names_registry), and that is the more
# precise complaint. Reporting a missing platform as well would ask the author
# to fix the wrong field.
_cloud_build_without_platform = lambda w: any -> bool {
    _rt = w.runtime
    _host = img_lib.registry_host(w.image if w.image else "")
    bool(_rt and _rt.type == "cluster" and not _rt.target.platform \
        and _host and not is_local_registry(_host) and builds_an_image(w))
}

# ---------------------------------------------------------------------------
# WHO APPLIES: the Bundle.lifecycle declaration's support.
#
# A declared lifecycle ("local" / "ephemeral") claims forge may apply to this
# env DIRECTLY. That claim is only honest for a cluster forge itself creates
# and can throw away, so the Bundle check below tests it against every
# kubectl context the env's objects actually land on.
# ---------------------------------------------------------------------------

# Every kubectl context this env's objects target, deduplicated and sorted.
#
# EVERY placement, not just the workloads': a declaration is only as strong
# as its weakest target, and an env that runs one `forge.Manifests` group or
# one Gateway on a cloud cluster is applying to that cloud cluster. Reading
# only `cluster_target` would let such an env claim local while deploying to
# GKE, which is exactly the hole this check exists to close.
# Every field is passed explicitly rather than taking the Bundle, because a
# schema `check:` block has no `self` — it reads its own fields by bare name,
# so a helper it calls must be given them.
_targeted_contexts = lambda workloads: [any], databases: [any], cluster_target: any, \
                            gateways: [any], http_routes: [any], grpc_routes: [any], \
                            helm_charts: [any], manifests: [any], generated: [any], rendered_secrets: [any] -> [str] {
    _from_workloads = [w.runtime.target.cluster for w in workloads \
        if w.runtime and w.runtime.type == "cluster" and w.runtime.target]
    _from_databases = [db.runtime.target.cluster for db in databases \
        if db.runtime and db.runtime.type == "cluster" and db.runtime.target]
    _from_primary = [cluster_target.cluster] if cluster_target else []
    # A Cluster REFERENCE denormalizes to its derived `.context`; a raw
    # forge.Manifests states the context as a string.
    _from_gateways = [g.cluster.context for g in gateways if g.cluster]
    _from_http = [r.cluster.context for r in http_routes if r.cluster]
    _from_grpc = [r.cluster.context for r in grpc_routes if r.cluster]
    _from_charts = [_eso_context(h) for h in helm_charts if h.cluster]
    _from_manifests = [m.cluster for m in manifests if m.cluster]
    _from_generated = [g.cluster for g in generated if g.cluster]
    _from_secrets = [s.cluster for s in rendered_secrets if s.cluster]
    _all = _from_workloads + _from_databases + _from_primary + _from_gateways \
        + _from_http + _from_grpc + _from_charts + _from_manifests + _from_generated + _from_secrets
    sorted([c for c in {x: 0 for x in _all if x}])
}

# A context forge CREATES and can delete: a k3d cluster.
#
# Two ways to qualify, because forge has two ways to own a local cluster and
# both are legitimate:
#
#   1. it is declared in `Bundle.clusters` with `provider = "k3d"` — the
#      declarative path, where `forge env up` reconciles the cluster at the
#      head of the run;
#   2. its context follows k3d's own `k3d-<name>` convention — the
#      `deploy/k3d.yaml` path the scaffolded dev env uses, where the cluster
#      is created by `forge cluster up` and so is never a Bundle.clusters
#      entry. kcl/lib/gateway.k's _is_local_cluster already treats this
#      prefix as the local-cluster signal; using a second, stricter rule
#      here would refuse the env forge itself scaffolds.
#
# A GKE / VKE / EKS context satisfies neither, which is the load-bearing
# half: a cloud env can never declare itself local to keep direct apply.
_is_forge_managed_context = lambda ctx: str, clusters: [any] -> bool {
    _declared = any c in clusters {
        c.provider == "k3d" and c.context == ctx
    }
    bool(_declared or ctx.startswith("k3d-"))
}

# The targeted contexts a declared lifecycle cannot account for. Empty => the
# declaration is true of every cluster this env applies to.
_unmanaged_contexts = lambda clusters: [any], workloads: [any], databases: [any], \
                             cluster_target: any, gateways: [any], http_routes: [any], \
                             grpc_routes: [any], helm_charts: [any], manifests: [any], \
                             generated: [any], rendered_secrets: [any] -> [str] {
    [c for c in _targeted_contexts(workloads, databases, cluster_target, gateways, \
            http_routes, grpc_routes, helm_charts, manifests, generated, rendered_secrets) \
        if not _is_forge_managed_context(c, clusters)]
}

# The env's primary cluster target: declared, else the target of the first
# cluster-bound workload (so an env whose workloads all bind one cluster
# need not name it twice), else none.
_primary_target = lambda b: Bundle -> any {
    _bound = [w.runtime.target for w in b.workloads if w.runtime and w.runtime.type == "cluster"]
    b.cluster_target if b.cluster_target else (_bound[0] if _bound else Undefined)
}

# ---------------------------------------------------------------------------
# Probe policy (ADR 0002 §5), in ONE place.
#
# A SERVICE that forge BUILDS serves /readyz + /healthz through serverkit, so
# the lowering writes that probe into the spec EXPLICITLY — paths and port —
# unless the author declared one. A forge-built OPERATOR that declares ports
# gets the same: its subcommand runs the manager under serverkit.Run, which
# serves /healthz + /readyz on the process PORT (its http port) whatever it
# mounts. With no ports there is no listener to name, so none is written. A CR on the wire is then self-describing:
# the hosted platform, the CLI renderer and a human reading the record all
# see the same probe, and nothing is inferred downstream. A third-party
# image (no build) gets none here: forge cannot know it serves /readyz, and
# Go's WorkloadSpec.EffectiveProbes gives a service with ports TCP on its
# first one. A worker or job gets none unless it declares one.
# ---------------------------------------------------------------------------
#
# The default reads the SAME config the process does: serverkit serves its
# probes on the paths in LIVENESS_PATHS / READINESS_PATHS (the scaffold's
# liveness_paths / readiness_paths fields, default /healthz and /readyz) and
# on the port in PORT, so the default probes the first path of each on that
# port, read from the workload's own env. The pod's probe and the process's
# route cannot drift apart.
_default_probes = lambda w: any -> any {
    _ready = _probe_path(w, "READINESS_PATHS", "/readyz")
    w.probes if w.probes else (_TProbes {
        # startupPath = readiness holds liveness off until the server is
        # ready, for the whole startup-dependency wait (the database connect
        # retries for up to DB_CONNECT_TIMEOUT). The window is v1alpha1's
        # DefaultStartupFailureThreshold.
        startupPath = _ready
        readinessPath = _ready
        livenessPath = _probe_path(w, "LIVENESS_PATHS", "/healthz")
        port = _probe_port(w)
    } if w.build and (w.kind == "service" or (w.kind == "operator" and w.ports)) else Undefined)
}

# A plain string env value of the workload, or "" (unset, or a reference
# the lowering cannot read: a secret, a field ref).
_env_str = lambda w: any, name: str -> str {
    _v = (w.env or {})[name] if name in (w.env or {}) else None
    _v if typeof(_v) == "str" else ""
}

# The first path of a comma-separated probe path list in env `name` — what
# serverkit.ProbePathList parses — else the default.
_probe_path = lambda w: any, name: str, default: str -> str {
    _paths = [_p.strip() for _p in _env_str(w, name).split(",") if _p.strip()]
    _paths[0] if _paths else default
}

# The port a default probe targets: the process's PORT when the workload
# sets it, else its http port.
_probe_port = lambda w: any -> int {
    _p = _env_str(w, "PORT")
    int(_p) if _p and _p.isdigit() else _http_port(w)
}

# The port a default probe targets: the one named `http`, else the first,
# else pkg/deploy's DefaultServicePort (a service with no ports is given
# `http` :8080 by the renderer).
_http_port = lambda w: any -> int {
    _named = [p.port for p in (w.ports or []) if p.name == "http"]
    _named[0] if _named else ((w.ports or [])[0].port if w.ports else 8080)
}

# ---------------------------------------------------------------------------
# Env: the authoring map -> the wire's list, one channel per entry.
# ---------------------------------------------------------------------------

# The channel of one env value, by its declared type.
_channel = lambda v: any -> str {
    "value" if typeof(v) == "str" else {
        "SecretRef" = "secretRef"
        "ConfigMapRef" = "configMapRef"
        "FieldRef" = "fieldRef"
        "ManagedSecret" = "managedSecret"
        "DatabaseRef" = "databaseRef"
        "WorkloadURL" = "workloadURL"
        "IdentityRef" = "identityRef"
    }[typeof(v)]
}

# One map entry -> one tiers.EnvVar, lowered for the referrer's runtime
# `rt`: a hosted referrer lowers a config-projected secret to its store key
# as a managedSecret and KEEPS a WorkloadURL; every other referrer resolves
# the WorkloadURL, through its cluster's host gateway when it is a pod.
_env_var = lambda name: str, v0: any, where: str, rt: any, bundle: Bundle -> any {
    hosted = rt.type == "hosted"
    v = _idn_resolve(v0, where, rt, bundle) if typeof(v0) == "IdentityRef" else v0
    _c = _channel(v)
    _TEnvVar {
        name = name
        if _c == "value":
            value = v
        if _c == "secretRef" and hosted and v.store_key:
            managedSecret = _TManagedSecretRef {name = v.store_key, optional = v.optional if v.optional else Undefined}
        if _c == "secretRef" and not (hosted and v.store_key):
            secretRef = _TSecretKeyRef {name = v.name, key = v.key, optional = v.optional if v.optional else Undefined}
        if _c == "configMapRef":
            configMapRef = _TConfigMapKeyRef {name = v.name, key = v.key}
        if _c == "fieldRef":
            fieldRef = _TFieldRef {fieldPath = v.path}
        if _c == "managedSecret":
            managedSecret = _TManagedSecretRef {name = v.name, optional = v.optional if v.optional else Undefined}
        if _c == "databaseRef":
            databaseRef = _TDatabaseRef {name = v.name, key = v.key if v.key else Undefined}
        if _c == "workloadURL" and hosted:
            workloadURL = _TWorkloadURLRef {name = _require_hosted_url_target(v.name, where, bundle)}
        if _c == "workloadURL" and not hosted:
            value = _require_workload_url(v.name, where, bundle, rt.target.host_gateway if rt.type == "cluster" else "localhost")
    }
}

# ---------------------------------------------------------------------------
# Derived env: the values forge writes for a key the author left unset.
#
# An author's own value always wins, so each of these is a DEFAULT, never an
# override. "Unset" means absent or an empty literal: an empty
# OTEL_SERVICE_NAME or GOMEMLIMIT means "no value" to the process reading it,
# and a reference (SecretRef, ...) counts as set whatever it resolves to.
#
#   OTEL_SERVICE_NAME — the workload's name. A project-wide default
#     ("unknown", or the project name) files every workload's spans,
#     metrics and errors under ONE service, which is exactly the attribution
#     an incident needs and loses. The workload name is unique per env by
#     construction. Every runtime that runs a process (host, cluster,
#     hosted) gets it; a tool and a build-only artifact run none.
#
#   OTEL_RESOURCE_ATTRIBUTES — the resource every signal is filed under:
#     deployment.environment.name=<Bundle.env>, service.namespace=<project>,
#     service.version=<Bundle.release>, each key omitted when its source is
#     empty (forge reports only what it knows). The runtime merges it into
#     its OpenTelemetry resource, and the environment name is how a backend
#     tells staging's spans from prod's. Same runtimes as OTEL_SERVICE_NAME.
#     ALL-OR-NOTHING: an author's value is kept whole, never spliced into,
#     because merging keys into a string they wrote would rewrite it. Values
#     are percent-encoded (W3C baggage), so a `,` or `=` in a project or env
#     name cannot split an attribute.
#
#   GOMEMLIMIT — for a Go process in a container with a memory limit (a
#     cluster or hosted runtime), `go_memory_limit_percent` of that limit,
#     rounded down to whole MiB. The Go runtime does not read its cgroup
#     limit: left alone, its heap grows toward GOGC's target and the kernel
#     OOM-kills the pod before the collector feels any pressure. Just below
#     the limit, the collector works harder instead. Not derived when the
#     author sets GOMEMLIMIT or GOGC: a workload that tunes its own GC owns
#     all of it. Not derived on host or compose: there is no container limit
#     to derive it from.
# ---------------------------------------------------------------------------

# The default `go_memory_limit_percent` for a workload forge builds with
# forge.GoBuild.
DEFAULT_GO_MEMORY_LIMIT_PERCENT: int = 90

_RUNS_A_PROCESS = ["host", "cluster", "hosted"]
_HAS_MEMORY_LIMIT = ["cluster", "hosted"]
_MIB = 1048576

# Whether the author set env key k: present, and not an empty literal.
_env_set = lambda env: {str:}, k: str -> bool {
    k in env and not (typeof(env[k]) == "str" and env[k] == "")
}

# The workload's go_memory_limit_percent: declared, else the GoBuild default,
# else 0 (forge cannot see that a ShellBuild, sibling image or third-party
# image is a Go binary).
_go_memory_limit_percent = lambda w: any -> int {
    w.go_memory_limit_percent if w.go_memory_limit_percent != Undefined and w.go_memory_limit_percent != None \
        else (DEFAULT_GO_MEMORY_LIMIT_PERCENT if w.build and w.build.type == "go" else 0)
}

# The container memory limit the renderer applies: the declared limit, else
# the request (v1alpha1.Resources.WithDefaults sets an unset limit to the
# request), else the default request.
_memory_limit_bytes = lambda w: any -> int {
    _r = w.resources if w.resources else _TResources {}
    _r.memoryLimitBytes if _r.memoryLimitBytes else _r.memoryRequestBytes
}

# GOMEMLIMIT for w on runtime rt, or "" when none is derived.
_gomemlimit = lambda w: any, rt: any -> str {
    _env = w.env if w.env else {}
    _pct = _go_memory_limit_percent(w)
    _mib = _memory_limit_bytes(w) * _pct // 100 // _MIB if _pct > 0 and rt.type in _HAS_MEMORY_LIMIT else 0
    "${_mib}MiB" if _mib > 0 and not _env_set(_env, "GOMEMLIMIT") and not _env_set(_env, "GOGC") else ""
}

# A resource-attribute value, percent-encoded so it cannot split the list.
_otel_encode = lambda v: str -> str {
    v.replace("%", "%25").replace(",", "%2C").replace("=", "%3D").replace(" ", "%20")
}

# OTEL_RESOURCE_ATTRIBUTES for the bundle: only the keys whose source is set.
# service.namespace is the project, which Bundle requires.
_otel_resource_attributes = lambda bundle: Bundle -> str {
    ",".join(
        (["deployment.environment.name=${_otel_encode(bundle.env)}"] if bundle.env else []) \
        + ["service.namespace=${_otel_encode(bundle.project)}"] \
        + (["service.version=${_otel_encode(bundle.release)}"] if bundle.release else [])
    )
}

# ---------------------------------------------------------------------------
# Observability: the typed destination (Bundle.observability).
#
# Apps export OTLP to a LOCAL endpoint and never learn the backend. What that
# endpoint is depends on the runtime the workload is bound to:
#
#   host     the local collector's loopback port (Observability.local_port);
#            the env's compose collector stores locally or forwards to
#            `destination`
#   cluster  the node-local Service of the agent DaemonSet forge renders
#            (an ObservabilityAgent record, expanded in Go)
#   hosted   NONE. The platform runs its own agent and injects the endpoint;
#            forge states only the protocol and the signals that are off
#
# An env that declares no `observability` (or enabled = False) exports
# nothing: no endpoint is injected, so the runtime's export stays off.
# ---------------------------------------------------------------------------

_obs_on = lambda b: Bundle -> bool {
    bool(b.observability and b.observability.enabled)
}

# The opt-out vocabulary. `enabled = False` opts out of all of it. These are
# the keys of the release shape's observability_policy and, joined, the value
# of the platform's namespace label.
OBS_CATEGORIES = ["browser", "logs", "metadata", "metrics", "profiles", "traces"]

# The categories the author explicitly opted out of, sorted. Profiles counts
# only when set False: unset follows where the store is (off in a cluster by
# default), which is not an opt-out. [] = nothing opted out.
_obs_category_off = lambda s: any, c: str -> bool {
    (c == "traces" and not s.traces) or (c == "metrics" and not s.metrics) or (c == "logs" and not s.logs) or (c == "metadata" and not s.metadata) or (c == "browser" and not s.browser) or (c == "profiles" and s.profiles == False)
}
_obs_disabled = lambda b: Bundle -> [str] {
    _s = b.observability.signals if b.observability else Undefined
    ([] if not b.observability else (OBS_CATEGORIES if not b.observability.enabled else [c for c in OBS_CATEGORIES if _obs_category_off(_s, c)]))
}

# Browser telemetry is off: the author disabled it, or everything.
_obs_browser_off = lambda b: Bundle -> bool {
    bool(b.observability) and (not b.observability.enabled or not b.observability.signals.browser)
}

# The base URL of the collector on this machine, for host processes and
# frontend dev servers. "" when the env has none.
_obs_local_endpoint = lambda b: Bundle -> str {
    "http://127.0.0.1:${b.observability.local_port}" if _obs_on(b) and b.observability.local_port > 0 else ""
}

# The node-local agent Service, as a workload in the cluster dials it. Only an
# env with a destination has an agent: the local ClickStack publishes on
# loopback, which a cluster cannot reach, so without a destination a cluster
# workload exports nothing (telemetry off, no dangling endpoint).
_obs_agent_endpoint = lambda b: Bundle -> str {
    "http://${OTEL_AGENT_SERVICE}.${b.observability.agent.namespace}.svc.cluster.local:4318" if _obs_on(b) and b.observability.destination else ""
}

# Profiles. Unset `signals.profiles` follows where the store is: ON with the
# local ClickStack (no destination, and the env publishes a profiles collector
# on `profiles_port`), OFF otherwise. An explicit value always wins.
_obs_profiles_on = lambda b: Bundle -> bool {
    _o = b.observability
    _obs_on(b) and (bool(_o.signals.profiles) if _o.signals.profiles in [True, False] else (not _o.destination and _o.profiles_port > 0))
}

# The full URL host processes push profiles to: the local profiles collector,
# which is not ClickStack's (that one cannot write profiles), so the base
# endpoint will not do. "" with a destination: there the forwarder takes
# profiles at the base endpoint like every other signal.
_obs_profiles_local_endpoint = lambda b: Bundle -> str {
    "http://127.0.0.1:${b.observability.profiles_port}/v1development/profiles" if _obs_profiles_on(b) and not b.observability.destination and b.observability.profiles_port > 0 else ""
}

_OTLP_TRANSPORT_KEYS: [str] = [
    "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_PROTOCOL",
    "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
    "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
    "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
]

# An author who set the endpoint or protocol (for any signal) made the
# transport decision: the pair is derived whole or not at all, so a gRPC
# endpoint is never paired with our http/protobuf.
_otlp_transport_set = lambda env: {str:} -> bool {
    any k in _OTLP_TRANSPORT_KEYS { _env_set(env, k) }
}

# The OTEL_* keys the declaration derives for a workload on rt.
_obs_env = lambda rt: any, b: Bundle, env: {str:} -> {str:str} {
    _ep = _obs_agent_endpoint(b) if rt.type == "cluster" else (_obs_local_endpoint(b) if rt.type == "host" else "")
    _transport = not _otlp_transport_set(env)
    _sig = b.observability.signals if _obs_on(b) else Undefined
    _pep = _obs_profiles_local_endpoint(b) if rt.type == "host" else ""
    ({OTEL_EXPORTER_OTLP_ENDPOINT = _ep, OTEL_EXPORTER_OTLP_PROTOCOL = "http/protobuf"} if _obs_on(b) and _ep and _transport else {}) \
        | ({OTEL_EXPORTER_OTLP_PROTOCOL = "http/protobuf"} if _obs_on(b) and rt.type == "hosted" and _transport else {}) \
        | ({OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT = _pep} if _pep and _transport and not _env_set(env, "OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT") else {}) \
        | ({OTEL_SDK_DISABLED = "true"} if b.observability and not b.observability.enabled and not _env_set(env, "OTEL_SDK_DISABLED") else {}) \
        | ({OTEL_PROFILES_EXPORTER = "none"} if _sig and not _obs_profiles_on(b) and not _env_set(env, "OTEL_PROFILES_EXPORTER") else {}) \
        | ({OTEL_TRACES_EXPORTER = "none"} if _sig and not _sig.traces and not _env_set(env, "OTEL_TRACES_EXPORTER") else {}) \
        | ({OTEL_METRICS_EXPORTER = "none"} if _sig and not _sig.metrics and not _env_set(env, "OTEL_METRICS_EXPORTER") else {}) \
        | ({OTEL_LOGS_EXPORTER = "none"} if _sig and not _sig.logs and not _env_set(env, "OTEL_LOGS_EXPORTER") else {}) \
        | _obs_sampler_env(b, env)
}

# HEAD sampling, in the SDK, so every service follows the root span's decision
# and a trace is whole or absent. Only a ratio below 1 is rendered (1 is the
# SDK's own default, parentbased_always_on) and an author's sampler wins whole:
# the sampler and its argument are a pair.
_obs_sampler_env = lambda b: Bundle, env: {str:} -> {str:str} {
    _r = b.observability.sampling.traces_ratio if _obs_on(b) else 1.0
    {
        OTEL_TRACES_SAMPLER = "parentbased_traceidratio"
        OTEL_TRACES_SAMPLER_ARG = str(_r)
    } if _r < 1.0 and b.observability.signals.traces and not _env_set(env, "OTEL_TRACES_SAMPLER") and not _env_set(env, "OTEL_TRACES_SAMPLER_ARG") else {}
}

# Workloads that ship their own OTLP logs while the env's collector already
# ships them: every line would be stored twice.
_logs_double_shipped = lambda workloads: [any], o: any -> [str] {
    [w.name for w in workloads if o and o.enabled and o.signals.logs and w.env and "OTEL_LOGS_EXPORTER" in w.env \
        and typeof(w.env["OTEL_LOGS_EXPORTER"]) == "str" and "otlp" in w.env["OTEL_LOGS_EXPORTER"]]
}

_cluster_targets = lambda workloads: [any] -> [any] {
    [w.runtime.target for w in workloads if w.runtime and w.runtime.type == "cluster" and w.kind != "tool"]
}

# The destination's credential reference: a bearer token file wins over a
# headers blob (the two are mutually exclusive in ObservabilityDestination).
_destination_secret = lambda o: any -> any {
    (_obs_token_secret(o) or o.destination.headers_secret) if o and o.destination else None
}

# The bearer-token Secret the node agent reads: the one the author named, else
# the one forge declares as a ControlPlaneSecret for a control-plane
# destination.
_obs_token_secret = lambda o: any -> any {
    (o.destination.token_secret or (ObservabilitySecretRef {
        name = OTEL_AGENT_TOKEN_SECRET
        key = CONTROL_PLANE_SECRET_KINDS["telemetry_token"][1]
    } if o.destination.token_from_control_plane else Undefined)) if o and o.destination else Undefined
}

# The credential Secret must be supplied wherever the agent will read it, or the
# DaemonSet never starts. Nothing to check without a cluster workload (no
# agent) or without a credential secret.
_headers_secret_supplied = lambda workloads: [any], o: any, required: [any], rendered: [any], delivered: [any] -> bool {
    _ref = _destination_secret(o)
    _need = bool(o and o.enabled and _ref and len(_cluster_targets(workloads)) > 0)
    (not _need) or any s in required { s.name == _ref.name and s.namespace == o.agent.namespace and _ref.key in s.keys } \
        or any s in rendered { s.name == _ref.name and s.namespace == o.agent.namespace and _ref.key in s.keys } \
        or any s in delivered { s.name == _ref.name and _cps_ns(s) == o.agent.namespace and _ref.key in CONTROL_PLANE_SECRET_KINDS[s.kind] }
}

# The refusal text, built only when the rule fires: a Bundle message is
# interpolated even when its condition holds, and an unset observability has
# no destination to name.
_headers_secret_msg = lambda o: any -> str {
    _ref = _destination_secret(o)
    _field = "token_secret" if o and o.destination and o.destination.token_secret else "headers_secret"
    ("Bundle.observability.destination.${_field} '${_ref.name}' (key '${_ref.key}') is not supplied for the node agent in namespace '${o.agent.namespace}'. Declare it so the deploy can check it: `required_secrets = [forge.ExternalSecret {name = \"${_ref.name}\", namespace = \"${o.agent.namespace}\", keys = [\"${_ref.key}\"]}]` for a Secret you provision out of band, a `rendered_secrets` entry in that namespace for a local env, or (for a token the control plane renews) `control_plane_secrets = [forge.ControlPlaneSecret {name = \"${_ref.name}\", namespace = \"${o.agent.namespace}\", cluster = <target>}]`") if _ref else ""
}

# ---------------------------------------------------------------------------
# forge.ControlPlaneSecret: a Secret the declared control plane supplies and
# renews. The author's declarations plus the ones forge derives.
# ---------------------------------------------------------------------------

_cps_ns = lambda s: ControlPlaneSecret -> str {
    s.namespace or s.cluster.namespace
}

# One per cluster that runs the node agent when the destination is the control
# plane's ingest: that is the Secret the agent reads its token from.
_cps_derived = lambda workloads: [any], o: any -> [ControlPlaneSecret] {
    _on = bool(o and o.enabled and o.destination and o.destination.token_from_control_plane)
    _targets = {t.cluster: t for t in _cluster_targets(workloads)} if _on else {}
    [ControlPlaneSecret {
        name = OTEL_AGENT_TOKEN_SECRET
        cluster = t
        namespace = o.agent.namespace
        reason = "node agent OTLP credential (Observability.destination.token_from_control_plane)"
    } for _, t in _targets]
}

_cps_all = lambda workloads: [any], o: any, declared: [ControlPlaneSecret] -> [ControlPlaneSecret] {
    declared + _cps_derived(workloads, o)
}

_cps_key = lambda s: ControlPlaneSecret -> str {
    "${s.cluster.cluster}/${_cps_ns(s)}/${s.name}"
}

# The connected cluster a context is bound to, by any of the places an env
# names one (the same set the Go side merges: the secret's own cluster, the
# primary target, a workload's target, Bundle.connected_clusters). "" = none.
_cps_connected = lambda ctx: str, own: any, workloads: [any], cluster_target: any, connected_clusters: {str:str} -> str {
    _from_workloads = [w.runtime.target.connected_cluster for w in workloads if w.runtime and w.runtime.type == "cluster" and w.runtime.target.cluster == ctx and w.runtime.target.connected_cluster]
    (own.connected_cluster if own.connected_cluster else \
        (connected_clusters[ctx] if ctx in connected_clusters and connected_clusters[ctx] else \
            (cluster_target.connected_cluster if cluster_target and cluster_target.cluster == ctx and cluster_target.connected_cluster else \
                (_from_workloads[0] if len(_from_workloads) > 0 else ""))))
}

_cps_unbound = lambda workloads: [any], o: any, declared: [ControlPlaneSecret], cluster_target: any, connected_clusters: {str:str} -> [ControlPlaneSecret] {
    [s for s in _cps_all(workloads, o, declared) if not _cps_connected(s.cluster.cluster, s.cluster, workloads, cluster_target, connected_clusters)]
}

_cps_unbound_msg = lambda workloads: [any], o: any, declared: [ControlPlaneSecret], cluster_target: any, connected_clusters: {str:str} -> str {
    _bad = _cps_unbound(workloads, o, declared, cluster_target, connected_clusters)
    _ctxs = sorted({s.cluster.cluster: 0 for s in _bad})
    "Bundle.control_plane_secrets ${[s.name for s in _bad]} target cluster ${_ctxs}, which names no connected cluster. The control plane can only write a Secret into a cluster it holds a credential for, so there is nothing to deliver it with. Register the cluster once with `forge cluster connect <name> --context ${_ctxs[0] if len(_ctxs) > 0 else '<context>'} --env ${option('env') or '<env>'}`, then name it on the target (`forge.ClusterTarget {connected_cluster = \"<name>\"}`) or at cluster level (`connected_clusters = {\"${_ctxs[0] if len(_ctxs) > 0 else '<context>'}\": \"<name>\"}` on the Bundle). A k3d cluster is not exempt: delivery needs the control plane to reach the cluster"
}

_cps_duplicates = lambda workloads: [any], o: any, declared: [ControlPlaneSecret] -> [str] {
    _all = _cps_all(workloads, o, declared)
    sorted({_cps_key(s): 0 for s in _all if len([x for x in _all if _cps_key(x) == _cps_key(s)]) > 1})
}

_cps_second_telemetry_token = lambda workloads: [any], o: any, declared: [ControlPlaneSecret] -> [str] {
    _all = [s for s in _cps_all(workloads, o, declared) if s.kind == "telemetry_token"]
    sorted({"${s.cluster.cluster}/${_cps_ns(s)}": 0 for s in _all if len([x for x in _all if x.cluster.cluster == s.cluster.cluster and _cps_ns(x) == _cps_ns(s) and x.name != s.name]) > 0})
}

# A control-plane Secret that something else ALSO provides. One Secret has one
# provider.
_cps_also_provided = lambda workloads: [any], o: any, declared: [ControlPlaneSecret], required: [any], rendered: [any], cluster_target: any -> [str] {
    sorted({_cps_key(s): 0 for s in _cps_all(workloads, o, declared) if \
        any r in required { r.name == s.name and r.namespace == _cps_ns(s) } \
        or any r in rendered { r.name == s.name and (r.namespace or (cluster_target.namespace if cluster_target else "")) == _cps_ns(s) }})
}

# The telemetry_token a cluster workload reads, if one is delivered to its
# cluster and namespace. Empty when none, or when the workload does not run on a
# cluster.
_cps_token_for = lambda w: any, rt: any, bundle: Bundle -> [ControlPlaneSecret] {
    [s for s in _cps_all(bundle.workloads, bundle.observability, bundle.control_plane_secrets) if rt.type == "cluster" and s.kind == "telemetry_token" and s.cluster.cluster == rt.target.cluster and _cps_ns(s) == rt.target.namespace]
}

# Whether forge wires the credential into w: it does unless the author set the
# headers themselves (an author's value always wins).
_cps_wires = lambda w: any, rt: any, bundle: Bundle -> bool {
    _env = w.env if w.env else {}
    _runs = rt.type == "cluster" and w.kind != "tool"
    _taken = len([v for v in (w.volumes if w.volumes else []) if v.name == CONTROL_PLANE_OTLP_VOLUME]) > 0
    _runs and len(_cps_token_for(w, rt, bundle)) > 0 and not _env_set(_env, "OTEL_EXPORTER_OTLP_HEADERS") and not _env_set(_env, "OTEL_EXPORTER_OTLP_HEADERS_FILE") and not _taken
}

# The optional Secret volume a wired workload mounts. Optional, so its pods
# start before the control plane has delivered the Secret; a whole-directory
# mount, so the kubelet refreshes the file when the Secret is rewritten.
_cps_volume = lambda w: any, rt: any, bundle: Bundle -> [tiers.Volume] {
    [tiers.Volume {
        name = CONTROL_PLANE_OTLP_VOLUME
        mountPath = CONTROL_PLANE_OTLP_DIR
        readOnly = True
        source = tiers.VolumeSource {secret = tiers.SecretVolumeSource {
            name = s.name
            optional = True
            items = [tiers.KeyToPath {key = CONTROL_PLANE_SECRET_KINDS["telemetry_token"][0], path = CONTROL_PLANE_OTLP_FILE}]
        }}
    } for s in _cps_token_for(w, rt, bundle)] if _cps_wires(w, rt, bundle) else []
}

# Host processes need a loopback port to export to.
_host_without_local_port = lambda workloads: [any], o: any -> [str] {
    [w.name for w in workloads if o and o.enabled and o.local_port == 0 and w.runtime and w.runtime.type == "host" and w.kind != "tool"]
}

# Table TTL for the local ClickStack: the longest retention hint, in hours.
# "" = the backend's default. ClickStack takes ONE TTL for all its tables.
observability_ttl = lambda o: Observability -> str {
    _days = max([o.retention.traces_days, o.retention.metrics_days, o.retention.logs_days])
    "${_days * 24}h" if _days > 0 else ""
}

# The FORGE_PROFILES value for a Profiling: the selected types in the
# runtime's order (forge/pkg/observe), or "none".
_profiling_types = lambda p: Profiling -> str {
    _on = [t[0] for t in [["cpu", p.cpu], ["heap", p.heap], ["alloc", p.alloc], ["goroutine", p.goroutine], ["mutex", p.mutex], ["block", p.block]] if t[1]]
    ",".join(_on) if _on else "none"
}

# A workload's `profiling` override, as the variables forge/pkg/observe reads.
# Nothing when it is unset (the runtime's defaults apply); each key only where
# the workload's own env leaves it unset.
_profiling_env = lambda w: any, env: {str:} -> {str:str} {
    _p = w.profiling if w.profiling else None
    ({FORGE_PROFILES = _profiling_types(_p)} if _p and not _env_set(env, "FORGE_PROFILES") else {}) \
        | ({FORGE_PROFILES_HEAP_INTERVAL = _p.heap_interval} if _p and not _env_set(env, "FORGE_PROFILES_HEAP_INTERVAL") else {})
}

# The keys forge derives for w on rt (only those the author left unset).
_derived_env = lambda w: any, rt: any, bundle: Bundle -> {str:} {
    _env = w.env if w.env else {}
    _runs = rt.type in _RUNS_A_PROCESS and w.kind != "tool"
    _gml = _gomemlimit(w, rt) if _runs else ""
    ({OTEL_SERVICE_NAME = w.name} if _runs and not _env_set(_env, "OTEL_SERVICE_NAME") else {}) \
        | ({OTEL_RESOURCE_ATTRIBUTES = _otel_resource_attributes(bundle)} if _runs and not _env_set(_env, "OTEL_RESOURCE_ATTRIBUTES") else {}) \
        | (_obs_env(rt, bundle, _env) if _runs else {}) \
        | (_profiling_env(w, _env) if _runs else {}) \
        | ({OTEL_EXPORTER_OTLP_HEADERS_FILE = CONTROL_PLANE_OTLP_DIR + "/" + CONTROL_PLANE_OTLP_FILE} if _cps_wires(w, rt, bundle) else {}) \
        | ({GOMEMLIMIT = _gml} if _gml else {})
}

# The env w runs with on rt: its own, then the derived defaults. A blank
# author value keeps its position and takes the derived value.
_effective_env = lambda w: any, rt: any, bundle: Bundle -> {str:} {
    _gone = _idn_shadowed(w, rt, bundle)
    _own = {k: v for k, v in (w.env if w.env else {}) if k not in _gone} if _gone else (w.env if w.env else {})
    _idn = _idn_env(w, rt, bundle)
    # The identity's entries replace whatever the author left in the same
    # slot, in place. `|` refuses two values of different types, and a blank
    # or defaulted literal legitimately gives way to a reference, so the
    # identity layer is laid over by comprehension; everything else keeps
    # `|` and its rule that a blank author value keeps its position and takes
    # the derived value.
    _base = {k: (_idn[k] if k in _idn else v) for k, v in _own} if _idn else _own
    _rest = {k: v for k, v in _idn if k not in _own} if _idn else {}
    (_base | _derived_env(w, rt, bundle)) | _rest
}

# ---------------------------------------------------------------------------
# forge.ManagedIdentity: resolution, derivation and lowering.
#
# The declaration is small on purpose. Everything else is DERIVED here, from
# the env's own workloads, so nothing is copy-pasted and no app code changes:
#
#   applications  one native_broker `<env>:<workload>` for the workload named
#                 `api`, else one per service workload
#   consumers     every service workload on a runtime that can reach the
#                 instance (hosted identity: cluster, hosted; host-run
#                 identity: host, cluster)
#   deliveries    ControlPlaneSecrets of kind identity / identity_jwks /
#                 identity_machine_key, one set per cluster namespace that
#                 runs a consumer
#   config        each consumer's typed config fields, as env values:
#                   hosted referrer   a reserved managed secret
#                   cluster referrer  a key of the delivered Secret (a FILE,
#                                     re-read on change, for the broker token
#                                     and the JWKS snapshot)
#                   host-run identity the env's secret store, by env var name
# ---------------------------------------------------------------------------

_idn_rt = lambda id: any -> str {
    id.runtime.type if id.runtime else "hosted"
}

# The runtimes a consumer may run on, given where the instance runs.
_idn_reach = lambda id: any -> [str] {
    ["host", "cluster"] if _idn_rt(id) == "host" else ["cluster", "hosted"]
}

_idn_tok = lambda s: str -> str {
    regex.replace(s.upper(), r"[^A-Z0-9]+", "_")
}

_idn_secret = lambda id: any -> str {
    IDENTITY_SECRET_PREFIX + id.name
}

_idn_services = lambda workloads: [any], id: any -> [any] {
    [w for w in workloads if w.kind == "service" and w.runtime and w.runtime.type in _idn_reach(id)]
}

_idn_apps = lambda workloads: [any], id: any, env: str, project: str -> [IdentityApplication] {
    _svc = _idn_services(workloads, id)
    _api = [w for w in _svc if w.name == "api"]
    id.applications if id.applications else [IdentityApplication {
        name = "${env or project}:${w.name}"
        workload = w.name
        kind = "native_broker"
    } for w in (_api if _api else _svc)]
}

_idn_app_of = lambda apps: [any], workload: str -> str {
    _m = [a.name for a in apps if a.workload == workload]
    _m[0] if _m else ""
}

_idn_consumers = lambda workloads: [any], id: any, env: str, project: str -> [IdentityConsumer] {
    _apps = _idn_apps(workloads, id, env, project)
    _base = id.consumers if id.consumers else [IdentityConsumer {workload = w.name} for w in _idn_services(workloads, id)]
    [IdentityConsumer {
        workload = c.workload
        application = c.application or _idn_app_of(_apps, c.workload)
        if c.fields:
            fields = c.fields
        deliver_only = c.deliver_only
        omit = c.omit
    } for c in _base]
}

_idn_workload = lambda workloads: [any], name: str -> any {
    _m = [w for w in workloads if w.name == name]
    _m[0] if _m else Undefined
}

# Names a declaration points at that the env cannot honour. Empty = fine.
_idn_bad_apps = lambda workloads: [any], id: any, env: str, project: str -> [str] {
    [a.workload for a in _idn_apps(workloads, id, env, project) if len([w for w in workloads if w.name == a.workload and w.kind == "service"]) == 0] if id else []
}
_idn_bad_consumers = lambda workloads: [any], id: any, env: str, project: str -> [str] {
    [c.workload for c in _idn_consumers(workloads, id, env, project) if len([w for w in _idn_services(workloads, id) if w.name == c.workload]) == 0] if id else []
}

# The keys an application contributes to the delivered Secret.
_idn_app_keys = lambda a: any -> [str] {
    _t = _idn_tok(a.name)
    ["${_t}_CLIENT_ID", "${_t}_REDIRECT_URI"] + (["${_t}_BROKER_TOKEN", "${_t}_LOGIN_URI"] if a.kind == "native_broker" else [])
}

# The outputs one consumer is handed.
_idn_outputs = lambda c: any, apps: [any] -> [str] {
    _app = [a for a in apps if a.name == c.application]
    _inst = ["audience", "issuer", "jwks", "roles_claim", "scopes"]
    _web = ["client_id", "redirect_uri"]
    _native = ["broker_token", "idp_base", "login_uri"]
    [o for o in _inst + (_web + (_native if _app[0].kind == "native_broker" else []) if _app else []) if o not in c.omit]
}

# The config field an output fills for c, and its env var.
_idn_field = lambda c: any, o: str -> str {
    _f = {
        "audience" = c.fields.audience
        "broker_token" = c.fields.broker_token
        "client_id" = c.fields.client_id
        "idp_base" = c.fields.idp_base
        "issuer" = c.fields.issuer
        "jwks" = c.fields.jwks
        "login_uri" = c.fields.login_uri
        "redirect_uri" = c.fields.redirect_uri
        "roles_claim" = c.fields.roles_claim
        "scopes" = c.fields.scopes
    } if c.fields else {}
    _f[o] or IDENTITY_FIELDS[o]
}
_idn_envname = lambda c: any, o: str -> str {
    _idn_field(c, o).upper()
}

# Which lowering a referrer on rt gets: "store" (a host-run identity), "managed"
# (a hosted referrer), "secret" (a cluster referrer of a hosted identity).
_idn_lowering = lambda id: any, rt: any -> str {
    "store" if _idn_rt(id) == "host" else ("managed" if rt.type == "hosted" else "secret")
}

# --- the Secrets the control plane delivers ---------------------------------

_idn_cluster_consumers = lambda workloads: [any], id: any, env: str, project: str -> [any] {
    [c for c in _idn_consumers(workloads, id, env, project) if _idn_workload(workloads, c.workload) and _idn_workload(workloads, c.workload).runtime.type == "cluster"]
}

_idn_cps = lambda workloads: [any], id: any, env: str, project: str -> [ControlPlaneSecret] {
    _apps = _idn_apps(workloads, id, env, project)
    _cc = _idn_cluster_consumers(workloads, id, env, project) if _idn_rt(id) == "hosted" else []
    _targets = {"${_idn_workload(workloads, c.workload).runtime.target.cluster}/${_idn_workload(workloads, c.workload).runtime.target.namespace}": _idn_workload(workloads, c.workload).runtime.target for c in _cc}
    _keys = lambda t: any -> [str] {
        sorted({k: 0 for k in ["AUDIENCE", "ISSUER", "SCOPES"] + [k for c in _cc for a in _apps if c.application == a.name and "${_idn_workload(workloads, c.workload).runtime.target.cluster}/${_idn_workload(workloads, c.workload).runtime.target.namespace}" == "${t.cluster}/${t.namespace}" for k in _idn_app_keys(a)]})
    }
    ([ControlPlaneSecret {
        name = _idn_secret(id)
        cluster = t
        kind = "identity"
        keys = _keys(t)
        required = True
        reason = "managed identity '${id.name}' outputs (issuer, audience, applications)"
    } for _, t in _targets] if _targets else []) \
    + [ControlPlaneSecret {
        name = _idn_secret(id) + "-jwks"
        cluster = t
        kind = "identity_jwks"
        keys = ["jwks.json"]
        reason = "managed identity '${id.name}' signing-key snapshot (the fallback when the issuer is unreachable at boot)"
    } for _, t in _targets] \
    + [ControlPlaneSecret {
        name = m.key_secret
        cluster = m.cluster
        kind = "identity_machine_key"
        keys = ["key.json"]
        reason = "managed identity '${id.name}' machine user '${m.name}'"
    } for m in id.machine_users if m.key_secret and _idn_rt(id) == "hosted"]
}

# The author's control-plane secrets plus the ones the identity derives.
_cps_decl = lambda workloads: [any], id: any, env: str, project: str, declared: [ControlPlaneSecret] -> [ControlPlaneSecret] {
    declared + (_idn_cps(workloads, id, env, project) if id else [])
}

# --- lowering: an output as an env value ------------------------------------

_idn_key = lambda o: str, app: str -> str {
    "JWKS" if o == "jwks" else ({
        "audience" = "AUDIENCE"
        "issuer" = "ISSUER"
        "idp_base" = "ISSUER"
        "scopes" = "SCOPES"
        "client_id" = "${_idn_tok(app)}_CLIENT_ID"
        "redirect_uri" = "${_idn_tok(app)}_REDIRECT_URI"
        "login_uri" = "${_idn_tok(app)}_LOGIN_URI"
        "broker_token" = "${_idn_tok(app)}_BROKER_TOKEN"
    }[o] if o in ["audience", "issuer", "idp_base", "scopes", "client_id", "redirect_uri", "login_uri", "broker_token"] else "")
}

# An IdentityRef as the value its referrer reads. Only called with a bundle
# that declares an identity. `roles_claim` is a literal, and the rest lower to
# the channel the referrer's runtime reads (see _idn_lowering).
_idn_resolve = lambda v: any, where: str, rt: any, bundle: Bundle -> any {
    _id = bundle.identity
    assert _id, "${where}: forge.IdentityRef needs Bundle.identity (forge.ManagedIdentity): there is no identity to read ${v.output} from"
    _apps = _idn_apps(bundle.workloads, _id, bundle.env, bundle.project)
    _app = v.application or (_apps[0].name if len(_apps) == 1 else "")
    assert v.output not in IDENTITY_APP_OUTPUTS or _app, "${where}: forge.IdentityRef output '${v.output}' belongs to an application: name one with `application = ...` (declared: ${[a.name for a in _apps]})"
    _kind = _idn_lowering(_id, rt)
    _k = _idn_key(v.output, _app)
    _snap = v.output == "jwks"
    _name = IDENTITY_MANAGED_PREFIX + _idn_tok(_id.name) + "_" + _k
    _secret = _idn_secret(_id) + ("-jwks" if _snap else "")
    _skey = "jwks.json" if _snap else _k
    _managed = _TManagedIdSecret {name = _name, optional = True if _snap else Undefined}
    _cluster = _TIdSecretRef {name = _secret, key = _skey, optional = True if _snap else Undefined}
    _store = _TIdSecretRef {name = _idn_secret(_id), key = _k, optional = True if _snap else Undefined}
    "auto" if v.output == "roles_claim" else (_managed if _kind == "managed" else (_cluster if _kind == "secret" else _store))
}

# What the identity hand-set values would collide with.
_idn_hand_set = lambda env: {str:}, k: str -> bool {
    _t = typeof(env[k]) if k in env else "none"
    False if _t == "none" else ((env[k] != "") if _t == "str" else (False if _t == "IdentityRef" else (not env[k].store_key if _t == "SecretRef" else True)))
}

_idn_entries = lambda c: any, rt: any, bundle: Bundle -> {str:} {
    _id = bundle.identity
    _apps = _idn_apps(bundle.workloads, _id, bundle.env, bundle.project)
    _outs = _idn_outputs(c, _apps)
    _files = ["broker_token", "jwks"] if _idn_lowering(_id, rt) == "secret" else []
    {
        _idn_envname(c, o): ("auto" if o == "roles_claim" else IdentityRef {output = o, application = c.application}) for o in _outs if o not in _files
    } | {
        "${_idn_envname(c, o)}_FILE": (IDENTITY_DIR + "/broker_token" if o == "broker_token" else IDENTITY_JWKS_DIR + "/jwks.json") for o in _outs if o in _files
    }
}

# The env a consumer is given, after refusing a field the author also set.
_idn_env = lambda w: any, rt: any, bundle: Bundle -> {str:} {
    _id = bundle.identity
    _cs = [c for c in _idn_consumers(bundle.workloads, _id, bundle.env, bundle.project) if c.workload == w.name and not c.deliver_only] if _id else []
    _c = _cs[0] if _cs else Undefined
    _env = w.env if w.env else {}
    _outs = _idn_outputs(_c, _idn_apps(bundle.workloads, _id, bundle.env, bundle.project)) if _c else []
    _clash = ["${_idn_envname(_c, o)} (config field ${_idn_field(_c, o)})" for o in _outs if o in IDENTITY_CONFLICT_OUTPUTS and _idn_hand_set(_env, _idn_envname(_c, o))] if _c else []
    assert len(_clash) == 0, "workload '${w.name}' sets ${_clash} by hand AND Bundle.identity fills it. One source per field: drop the value from the workload's env / deploy/kcl/${bundle.env or '<env>'}/config.k and let the identity supply it, or remove Bundle.identity to bring your own issuer"
    _idn_entries(_c, rt, bundle) if _c else {}
}

# The env vars an identity FILE output replaces: the file wins, so the plain
# variable a config projection (or a hand) left behind must not stay beside it.
_idn_shadowed = lambda w: any, rt: any, bundle: Bundle -> [str] {
    _id = bundle.identity
    _cs = [c for c in _idn_consumers(bundle.workloads, _id, bundle.env, bundle.project) if c.workload == w.name and not c.deliver_only] if _id else []
    _c = _cs[0] if _cs else Undefined
    _outs = _idn_outputs(_c, _idn_apps(bundle.workloads, _id, bundle.env, bundle.project)) if _c else []
    [_idn_envname(_c, o) for o in ["broker_token", "jwks"] if o in _outs] if _c and _idn_lowering(_id, rt) == "secret" else []
}

# The Secret volumes a cluster consumer re-reads the broker token and the JWKS
# snapshot from. Whole-directory mounts: the kubelet refreshes them in place.
# The identity Secret is NOT optional, so the pod waits for the first delivery;
# the snapshot is a fallback, so its Secret is.
_idn_volumes = lambda w: any, rt: any, bundle: Bundle -> [tiers.Volume] {
    _id = bundle.identity
    _cs = [c for c in _idn_consumers(bundle.workloads, _id, bundle.env, bundle.project) if c.workload == w.name and not c.deliver_only] if _id and rt.type == "cluster" and _idn_rt(_id) == "hosted" else []
    _c = _cs[0] if _cs else Undefined
    _apps = _idn_apps(bundle.workloads, _id, bundle.env, bundle.project) if _c else []
    _outs = _idn_outputs(_c, _apps) if _c else []
    ([tiers.Volume {
        name = "forge-identity"
        mountPath = IDENTITY_DIR
        readOnly = True
        source = tiers.VolumeSource {secret = tiers.SecretVolumeSource {
            name = _idn_secret(_id)
            items = [tiers.KeyToPath {key = _idn_key("broker_token", _c.application), path = "broker_token"}]
        }}
    }] if "broker_token" in _outs else []) + ([tiers.Volume {
        name = "forge-identity-jwks"
        mountPath = IDENTITY_JWKS_DIR
        readOnly = True
        source = tiers.VolumeSource {secret = tiers.SecretVolumeSource {
            name = _idn_secret(_id) + "-jwks"
            optional = True
            items = [tiers.KeyToPath {key = "jwks.json", path = "jwks.json"}]
        }}
    }] if "jwks" in _outs else [])
}

# --- the shape's identity ---------------------------------------------------

_idn_managed_names = lambda b: Bundle -> [str] {
    _id = b.identity
    _apps = _idn_apps(b.workloads, _id, b.env, b.project)
    _hosted = [c for c in _idn_consumers(b.workloads, _id, b.env, b.project) if _idn_workload(b.workloads, c.workload) and _idn_workload(b.workloads, c.workload).runtime.type == "hosted"] if _idn_rt(_id) == "hosted" else []
    sorted({IDENTITY_MANAGED_PREFIX + _idn_tok(_id.name) + "_" + _idn_key(o, c.application): 0 for c in _hosted for o in _idn_outputs(c, _apps) if o != "roles_claim"})
}

_render_identity = lambda b: Bundle -> any {
    _id = b.identity
    _apps = _idn_apps(b.workloads, _id, b.env, b.project)
    _dl = [s for s in _idn_cps(b.workloads, _id, b.env, b.project) if s.kind in IDENTITY_SECRET_KINDS]
    {
        "name": _id.name
        "runtime": _idn_rt(_id)
        "share_with": _id.share_with if _id.share_with else None
        "project_name": _id.project_name if _id.project_name else None
        "login": {"mode": _id.login.mode}
        "public": (_id.public or _id.login.mode == "hosted") or None
        "domain": _id.domain if _id.domain else None
        "roles": [{
            "key": r.key
            "display_name": r.display_name if r.display_name else None
            "group": r.group if r.group else None
            "members_from": r.members_from
            "members": r.members if r.members else None
        } for r in _id.roles]
        "applications": [{
            "name": a.name
            "workload": a.workload
            "kind": a.kind
            "redirect_uris": a.redirect_uris if a.redirect_uris else None
            "logout_uris": a.logout_uris if a.logout_uris else None
            "login_uri": a.login_uri if a.login_uri else None
        } for a in _apps]
        "consumers": [{
            "workload": c.workload
            "application": c.application if c.application else None
            "fields": ({o: _idn_field(c, o) for o in IDENTITY_OUTPUTS if c.fields and _idn_field(c, o) != IDENTITY_FIELDS[o]} or None) if c.fields else None
            "deliver_only": True if c.deliver_only else None
            "omit": sorted(c.omit) if c.omit else None
        } for c in _idn_consumers(b.workloads, _id, b.env, b.project)]
        "machine_users": [{
            "name": m.name
            "roles": m.roles if m.roles else None
            "key_secret": {
                "name": m.key_secret
                "namespace": m.cluster.namespace
                "cluster": m.cluster.cluster
                "connected_cluster": _cps_connected(m.cluster.cluster, m.cluster, b.workloads, b.cluster_target, b.connected_clusters)
            } if m.key_secret else None
        } for m in _id.machine_users]
        "smtp": {
            "host": _id.smtp.host
            "port": _id.smtp.port
            "user": _id.smtp.user if _id.smtp.user else None
            "sender": _id.smtp.sender
            "password_secret": _id.smtp.password.name
        } if _id.smtp else None
        "seed_users": [{
            "email": u.email
            "roles": u.roles if u.roles else None
            "password_secret": u.password_secret.name if u.password_secret else None
        } for u in _id.seed_users]
        "deliveries": [{
            "kind": s.kind
            "name": s.name
            "namespace": _cps_ns(s)
            "cluster": s.cluster.cluster
            "connected_cluster": _cps_connected(s.cluster.cluster, s.cluster, b.workloads, b.cluster_target, b.connected_clusters)
        } for s in _dl]
        "managed_secrets": _idn_managed_names(b) or None
    }
}

# ---------------------------------------------------------------------------
# forge.WorkloadURL resolution (lib/workload_url.k has the order).
# ---------------------------------------------------------------------------

# The URL a reference resolves to at render, or a refusal naming it.
_require_workload_url = lambda name: str, where: str, bundle: Bundle, from_host: str = "localhost" -> str {
    _names = _env_names(bundle.workloads, bundle.infra, bundle.frontends)
    assert name in _names, \
        "${where}: forge.WorkloadURL names workload '${name}', which this environment does not declare. " \
        + "Declared workloads: " + ", ".join(_names) + "."
    _url = wurl_lib.resolve(name, bundle, from_host)
    assert _url, \
        "${where}: forge.WorkloadURL '${name}' has no URL forge can know at render time. " \
        + "Give it one: an HTTPRoute to it, a custom domain on its exposed port, `listen_ports` on its host runtime " \
        + "(forge env up assigns an undeclared host port only after rendering), or a frontend `port`."
    _url
}

# A HOSTED referrer keeps the reference, and the control plane resolves it
# against what IT runs. So the target must be hosted too: a host process or
# a cluster you operate is unreachable from the platform.
_require_hosted_url_target = lambda name: str, where: str, bundle: Bundle -> str {
    _names = _env_names(bundle.workloads, bundle.infra, bundle.frontends)
    assert name in _names, \
        "${where}: forge.WorkloadURL names workload '${name}', which this environment does not declare. " \
        + "Declared workloads: " + ", ".join(_names) + "."
    _hosted = _hosted_names(bundle.workloads, [], bundle.frontends)
    assert name in _hosted, \
        "${where}: forge.WorkloadURL names '${name}', which does not run on the hosted runtime. A hosted workload's references are resolved by the control plane against workloads IT runs, and it cannot reach a host process or a cluster you operate. Bind '${name}' to forge.OnHosted {} too, or pass its URL as a literal value."
    name
}

# ---------------------------------------------------------------------------
# The workload lowering.
# ---------------------------------------------------------------------------

# The registry-less artifact name a forge-built workload is built into —
# the build-state / release-ledger key, and the repository its build pushes to.
# "" when forge builds nothing for it.
#
# A declared image names it, with any `:tag` stripped: `reliant:e2e` is built
# as IMAGE=reliant, TAG=e2e (the Go build side reads the tag back off the
# resolved spec.image). It used to fall back to the output_name / workload
# name for ANY tagged image, so the build pushed `<registry>/<name>:<env tag>`
# while the deploy pulled `<registry>/reliant:e2e` — ErrImagePull. Only an
# UNSET image derives the name from the build.
_artifact = lambda w: any -> str {
    (img_lib.repository(w.image) if w.image \
        else (w.build.output_name if w.build.output_name else w.name)) if w.build else ""
}

# The build identity: the registry-less artifact plus the tag the workload's
# own image pins, if it pins one ("workspace-base:dev-per-daemon", "api"). It
# is a fact about the BUILD, so it is the same on every runtime — a BuildOnly
# or host workload's spec.image resolves no pull ref, and a pin read back off
# spec.image was silently lost there. "" when forge builds nothing for it.
_build_image = lambda w: any -> str {
    _name = _artifact(w)
    _tail = w.image[w.image.rfind("/") + 1:] if w.image else ""
    (_name + _tail[_tail.rfind(":"):] if _name and w.image and "@" not in w.image and ":" in _tail else _name)
}

# A build cannot produce a digest chosen in advance: a digest names bytes
# that already exist. Building to some tag the deploy then ignores, or
# skipping the declared build, would each leave the declaration lying, so a
# forge-built workload pinned to a digest is refused.
_check_built_digest = lambda w: any -> bool {
    assert not (w.build and w.image and "@" in w.image), \
        "workload '${w.name}': image '${w.image}' pins a digest, but the workload also declares a build. " \
        + "A build cannot produce a pre-chosen digest. Drop `build` to run that exact image, or drop the " \
        + "digest (pin a tag, or leave it unpinned) so the image is the one forge builds; " \
        + "`forge env deploy` pins what the build pushed by digest itself."
    True
}

# A workload's image must name its own registry, because the registry is
# declared THERE and nowhere else — no env-level field supplies one. An image
# with no host is an incomplete declaration, so it is refused at render,
# naming the workload and the runtime that needs it, rather than rendering a
# reference the kubelet resolves against Docker Hub and cannot pull.
#
# The two runtimes that PULL an image get different runbooks, because the
# registry an author should name is a different question on each.
# `image` may be "" for a runtime that resolves none (a host or compose
# workload, or a cluster workload forge builds nothing for); there is no
# reference to check, so both asserts stay inert.
_check_image_names_registry = lambda w: any, rt: any, image: str -> bool {
    assert not (image and rt.type == "cluster" and not img_lib.has_registry_host(image)), \
        "workload '${w.name}': image '${image}' names no registry host, and it is bound to a cluster runtime " \
        + "that must pull it. The registry is part of the image: declare the full reference on the workload, e.g. " \
        + "`image = \"ghcr.io/<owner>/${image}\"` (or `localhost:5051/${image}` for a k3d-local registry). " \
        + "There is no env-level registry — a ClusterTarget declares the cluster, namespace and platform; " \
        + "the workload declares what to pull."
    # forge.OnHosted DELIBERATELY DOES NOT REQUIRE A HOST (ADR-0003 F1).
    #
    # The registry lives on the image for every runtime whose registry the
    # AUTHOR chooses. A hosted workload is the one case where they do not: the
    # control plane admits images from exactly one subtree,
    # `<registry_base>/<org>`, and refuses every other — so a hosted author
    # writing a host was transcribing a value the platform already knew, and
    # getting it wrong produced a publish-time refusal.
    #
    # So a bare hosted image renders as-is and forge resolves it against the
    # base the control plane reports (internal/cli: resolveHostedImageBase).
    # It is NOT resolved here, because KCL has no network and a render must
    # stay an offline, reproducible projection — the full reference appears in
    # the build's push plan and in the deploy's pinned spec, both of which
    # read the base from the ensure they already make.
    True
}

# The image a runtime runs, resolved per runtime (module docstring). The
# author's registry is kept verbatim; forge contributes the tag and digest.
_spec_image = lambda w: any, rt: any, bundle: Bundle -> str {
    _name = w.image if w.image else _artifact(w)
    assert _check_image_names_registry(w, rt, _name)
    workload_image_ref_for(w.name, "container:" + w.name, _name, bundle.image_tag) if rt.type == "cluster" and _name \
        else (_name if rt.type == "hosted" else "")
}

# The spec, as the generated tiers.Workload. `rt` is the resolved runtime.
_spec = lambda w: any, rt: any, bundle: Bundle -> any {
    _env = _effective_env(w, rt, bundle)
    _TWorkload {
        kind = w.kind
        image = _spec_image(w, rt, bundle) or "-"
        command = w.command
        args = w.args
        replicas = w.replicas
        if w.resources:
            resources = w.resources
        env = [_env_var(k, v, "workload '${w.name}' env ${k}", rt, bundle) for k, v in _env] if _env else Undefined
        ports = w.ports
        probes = _default_probes(w)
        storageGiB = w.storageGiB
        schedule = w.schedule
        before = w.before
        deployPhase = w.deployPhase
        namespacedRBAC = w.namespacedRBAC
        clusterRBAC = w.clusterRBAC
        crds = w.crds
        group = w.group
        version = w.version
        leaderElection = w.leaderElection
        serviceAccount = w.serviceAccount
        serviceAccountAnnotations = w.serviceAccountAnnotations
        sidecars = [s | {image = workload_image_ref_for(w.name, "container:" + s.name, s.image, bundle.image_tag)} for s in w.sidecars] if w.sidecars else Undefined
        volumes = ((w.volumes if w.volumes else []) + _cps_volume(w, rt, bundle) + _idn_volumes(w, rt, bundle)) or Undefined
        nodeSelector = w.nodeSelector
        tolerations = w.tolerations
        podAnnotations = w.podAnnotations
        priorityClassName = w.priorityClassName
        enableServiceLinks = w.enableServiceLinks
        strategy = w.strategy
        securityContext = w.securityContext
        terminationGracePeriodSeconds = w.terminationGracePeriodSeconds
        activeDeadlineSeconds = w.activeDeadlineSeconds
    }
}

# The spec JSON: the generated projection, with the placeholder image of a
# runtime that runs none emptied. (tiers.Workload requires an image, which is
# right for the wire; host / compose / build-only have none to state.)
_spec_json = lambda w: any, rt: any, bundle: Bundle -> {str:} {
    _j = tiers.workload_json(_spec(w, rt, bundle))
    {k: ("" if k == "image" and v == "-" else v) for k, v in _j}
}

_render_runtime = lambda w: any, rt: any -> any {
    {
        "type": "host"
        "runner": rt.runner
        "air_config": rt.air_config if rt.air_config else None
        "working_dir": rt.working_dir if rt.working_dir else None
        # `is not Undefined`, NOT truthiness: an explicitly EMPTY list means
        # "binds nothing" and must reach Go distinct from "not declared".
        "listen_ports": rt.listen_ports if rt.listen_ports is not Undefined else None
        "delve_port": rt.delve_port
    } if rt.type == "host" else ({
        "type": "compose"
        "service": rt.service if rt.service else w.name
        "file": rt.file
        "env_file": rt.env_file if rt.env_file else None
        "wait": rt.wait
        "wait_timeout": rt.wait_timeout
        "shared": rt.shared
        # The compose PROCESS env: the workload's literal env under the
        # runtime's own (the runtime block is the more specific statement).
        "env": {k: v for k, v in w.env if typeof(v) == "str"} | rt.env
    } if rt.type == "compose" else ({
        "type": "cluster"
        "cluster": rt.target.cluster
        "namespace": rt.target.namespace
        "connected_cluster": rt.target.connected_cluster if rt.target.connected_cluster else None
        "domain": rt.target.domain if rt.target.domain else None
        "platform": rt.target.platform if rt.target.platform else None
        "image_pull_secrets": rt.target.image_pull_secrets if rt.target.image_pull_secrets else []
    } if rt.type == "cluster" else ({
        "type": "hosted"
        "platform": rt.platform
    } if rt.type == "hosted" else {
        "type": "build-only"
        "build_variants": [_render_build_variant(bv) for bv in rt.build_variants]
    })))
}

# One output.workloads[] entry. Runs the runtime mask first.
_lower = lambda w: any, bundle: Bundle -> any {
    _rt = _bound_runtime(w)
    _bad = _mask_violations(w, _rt)
    assert len(_bad) == 0, "workload '${w.name}' cannot run on the ${_rt.type} runtime:\n  - " + "\n  - ".join(_bad)
    assert _check_built_digest(w)
    {
        "name": w.name
        "kind": w.kind
        "image": _artifact(w)
        "build_image": _build_image(w) or None
        "build": _render_build(w.build)
        if w.release_dependencies is not Undefined:
            "release_dependencies": w.release_dependencies
        "release_inputs": {k: workload_image_ref_for(w.name, "input:" + k, v, bundle.image_tag) for k, v in w.release_inputs}
        "runtime": _render_runtime(w, _rt)
        "spec": _spec_json(w, _rt, bundle)
    } | ({"flow_checks": [{"path": c.path, "description": c.description or None, "name": c.name or None} for c in w.flow_checks]} if w.flow_checks else {}) \
      | ({"delivery_unit": w.delivery_unit} if w.delivery_unit else {})
}

# `before` names a workload the env declares, on the same runtime set that
# orders it (a Go renderer / host runner orders within the env).
_check_before = lambda workloads: [any] -> bool {
    _names = [w.name for w in workloads]
    _dangling = ["${w.name} -> ${b}" for w in workloads for b in (w.before or []) if b != BEFORE_ALL and b not in _names]
    assert len(_dangling) == 0, \
        "Workload.before names a workload this environment does not declare: " + ", ".join(_dangling) \
        + ". Declared workloads: " + ", ".join(_names) + "."
    True
}

# ShellBuild.built_images names a workload forge can build an image for in
# THIS environment: another ShellBuild workload that declares an image. The
# build resolves the edge to the digest that workload pushed, so an edge to a
# workload the env does not declare, to one built some other way, or to
# itself is refused here — at render, naming the edge — rather than at the
# build, after other images have already been pushed.
_check_built_images = lambda workloads: [any] -> bool {
    _builders = [w.name for w in workloads if w.build and w.build.type == "shell" and w.image]
    _edges = [[w.name, v, b] for w in workloads if w.build and w.build.type == "shell" for v, b in w.build.built_images]
    _bad = ["${e[0]}.${e[1]} -> ${e[2]}" for e in _edges if e[2] == e[0] or e[2] not in _builders]
    assert len(_bad) == 0, \
        "ShellBuild.built_images must name ANOTHER workload in this environment that is built by a ShellBuild and declares an image (forge passes it the digest that build pushed): " \
        + ", ".join(_bad) + ". ShellBuild workloads with an image: " + (", ".join(_builders) or "none") + "."
    True
}

# ---------------------------------------------------------------------------
# The RUNTIME MASK (ADR 0002 §3; P2 map §6.2).
#
# What each runtime can honour. `hosted` is GENERATED from Go's
# v1alpha1.FieldProfiles / KindProfiles (tiers/profiles_gen.k), so the
# default-deny table that gates the control plane also gates the author, and
# a field added to the spec is refused on hosted until Go classifies it.
# The Go gate (Workload.Validate(Restricted), server-side) stays the real
# one; this is the same answer at the line that caused it.
#
# host / compose / build-only are forge's own: each field they refuse would
# otherwise be silently ignored by the runtime.
# ---------------------------------------------------------------------------

# Spec fields a runtime that runs a local process or a compose service
# cannot honour, and why.
_LOCAL_REFUSED: {str:str} = {
    "storageGiB": "a local process has no volume to provision; use a host path in its config"
    "namespacedRBAC": "a local process has no Kubernetes identity to grant it to"
    "clusterRBAC": "a local process has no Kubernetes identity to grant it to"
    "serviceAccount": "a local process has no Kubernetes identity"
    "serviceAccountAnnotations": "a local process has no Kubernetes identity to annotate"
    "sidecars": "a local process has no pod to add a container to"
    "volumes": "a local process has no pod to mount into; it reads the host filesystem"
    "nodeSelector": "a local process is not scheduled onto nodes"
    "tolerations": "a local process is not scheduled onto nodes"
    "podAnnotations": "a local process has no pod to annotate"
    "priorityClassName": "a local process is not scheduled, so there is no pod to rank"
    "enableServiceLinks": "a local process has no pod, so Kubernetes injects no service links"
    "strategy": "a local process has no Deployment to roll out; forge env up starts it once"
    "securityContext": "a local process runs as the user who started it"
    "terminationGracePeriodSeconds": "a local process is stopped by forge env down, not by a kubelet"
}

_COMPOSE_REFUSED: {str:str} = _LOCAL_REFUSED | {
    "build": "compose builds its own image from the compose file; drop `build`, or bind a runtime forge builds for"
    "ports": "the compose file owns the published ports; declare them there"
    "probes": "the compose file owns the healthcheck; declare it there"
    "replicas": "the compose file owns the scale"
    "command": "the compose file owns the container's command"
    "args": "the compose file owns the container's command"
    "resources": "the compose file owns the container's resources"
    "schedule": "compose has no cron"
    "crds": "compose has no Kubernetes API"
}

_BUILD_ONLY_REFUSED: {str:str} = {
    "ports": "a build-only workload is never run, so nothing listens"
    "probes": "a build-only workload is never run, so there is nothing to probe"
    "replicas": "a build-only workload is never run"
    "storageGiB": "a build-only workload is never run"
}

_set = lambda w: any, f: str -> bool {
    _v = w[f]
    bool(_v) if f != "replicas" else _v != 1
}

# A hosted workload's ports may not declare custom `domains`.
#
# The name is not spec. Bringing your own hostname takes an action at YOUR
# registrar, ownership verification and a certificate — asynchronous and
# human-gated, none of which a deploy converges; it binds to ONE environment
# while a single workloads.k renders to many; and the platform may allocate a
# hostname itself, which a spec field would then contradict. So the domain
# lives in the control plane as its own resource, created once and bound per
# env.
#
# Refused HERE, in the render, rather than at deploy: `forge env render` and
# `forge env deploy --plan` have to fail too, or the declaration survives
# review and only fails against a live control plane.
#
# OnCluster is untouched — there you own the ingress, and forge's own
# Gateway/HTTPRoute serves `domains[0]` (kcl/lib/workload_url.k).
_hosted_domain_violations = lambda w: any -> [str] {
    _d = [d for p in (w.ports or []) for d in (p.domains or [])]
    ["domains ${_d}: a hosted custom hostname is a control-plane resource, not spec — it needs DNS at your registrar, ownership verification and a certificate, and it binds to ONE environment while this file renders to many. Create it once and bind it: `forge domain add ${_d[0]}`, then `forge domain bind ${_d[0]} --env <env> --target ${w.name}`. (forge.OnCluster keeps `Port.domains`: there you own the ingress.)"] if _d else []
}

_mask_violations = lambda w: any, rt: any -> [str] {
    _hosted = ["${f}: ${tiers.RESTRICTED_FIELD_REASONS[f]}" for f in tiers.RESTRICTED_FIELD_REASONS if f in w and _set(w, f)] \
        + ["env ${k} (${_channel(v)}): ${tiers.RESTRICTED_FIELD_REASONS['env.' + _channel(v)]}" for k, v in w.env if "env." + _channel(v) in tiers.RESTRICTED_FIELD_REASONS and not (_channel(v) == "secretRef" and v.store_key)] \
        + (["kind '${w.kind}' (${tiers.RESTRICTED_KIND_REASONS[w.kind]})"] if w.kind in tiers.RESTRICTED_KIND_REASONS else []) \
        + (["image: a hosted workload needs a build or an image to run"] if not w.build and not w.image else []) \
        + _hosted_domain_violations(w) \
        if rt.type == "hosted" else []
    _host = ["${f}: ${_LOCAL_REFUSED[f]}" for f in _LOCAL_REFUSED if _set(w, f)] \
        + (["replicas: a host workload is one local process; forge env up launches it once"] if w.replicas != 1 else []) \
        + (["build: the ${rt.runner} runner derives its command from a forge.GoBuild; add `build = forge.GoBuild {cmd = ...}`, or set `command` to run a binary verbatim"] \
            if rt.runner in ["go-run", "binary", "delve"] and not (w.build and w.build.type == "go") and not w.command else []) \
        + (["kind '${w.kind}': a local process cannot ${'run on a schedule' if w.kind == 'cron' else 'be a controller-runtime manager'}; bind it to forge.OnCluster"] if w.kind in ["cron", "operator"] else []) \
        if rt.type == "host" else []
    _compose = ["${f}: ${_COMPOSE_REFUSED[f]}" for f in _COMPOSE_REFUSED if _set(w, f)] \
        if rt.type == "compose" else []
    _build_only = ["${f}: ${_BUILD_ONLY_REFUSED[f]}" for f in _BUILD_ONLY_REFUSED if _set(w, f)] \
        + (["build: a build-only workload with no build has nothing to produce"] if not w.build else []) \
        if rt.type == "build-only" else []
    _hosted + _host + _compose + _build_only
}

# ---------------------------------------------------------------------------
# The applyable stream.
# ---------------------------------------------------------------------------

# One forge.dev/v1alpha1 Workload record: what pkg/deploy.RenderWorkloads
# expands (grouped by forge.dev/cluster + namespace), and the same spec the
# hosted path publishes. The spec is THE SAME VALUE as the entity's.
_workload_record = lambda entity: any, part_of: str -> any {
    {
        apiVersion = "forge.dev/v1alpha1"
        kind = "Workload"
        metadata = {
            name = entity.name
            namespace = entity.runtime.namespace
            labels = labels_lib.managed_labels(entity.name, part_of) | {"forge.dev/cluster" = entity.runtime.cluster} \
                | ({"forge.dev/delivery-unit" = entity.delivery_unit} if entity.delivery_unit else {})
        }
        spec = entity.spec
    }
}

# One forge.Manifests group, placed and (when named) owned. Every object is
# stamped with the group's RESOLVED cluster (its own, else the primary's),
# including on the primary: the deploy router replicates an object with no
# forge.dev/cluster to EVERY cluster of a multi-cluster env (it never guesses
# a primary for an unattributed object), so leaving the primary's objects
# unstamped put a primary-only ClusterIssuer on every cluster. Only an env
# with no cluster at all leaves the label off.
_raw_manifests = lambda g: any, primary: any, part_of: str -> [any] {
    _cluster = g.cluster if g.cluster else (primary.cluster if primary else "")
    _ns = g.namespace if g.namespace else (primary.namespace if primary else "")
    [_stamp_unit(_stamp_cluster(_default_namespace(_stamp_owner(m, g.name, part_of) if g.name else m, _ns), _cluster) \
        if _cluster else _default_namespace(_stamp_owner(m, g.name, part_of) if g.name else m, _ns), g.delivery_unit) \
        for m in g.objects]
}

# Place an object in a delivery unit (forge.DeliveryUnit). A label the author
# set on the object itself wins, like every other stamp here.
_stamp_unit = lambda manifest: any, unit: any -> any {
    _meta = manifest.metadata if manifest.metadata else {}
    _labels = _meta.labels if _meta.labels else {}
    manifest if not unit or _labels["forge.dev/delivery-unit"] else manifest | {metadata = _meta | {labels = _labels | {"forge.dev/delivery-unit" = unit}}}
}

# Cluster-scoped kinds a namespace must never be written onto.
_CLUSTER_SCOPED: [str] = [
    "Namespace", "CustomResourceDefinition", "ClusterRole", "ClusterRoleBinding",
    "ClusterIssuer", "StorageClass", "PriorityClass", "RuntimeClass",
    "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration", "APIService",
    "PersistentVolume", "GatewayClass", "IngressClass", "CSIDriver", "Node",
]

_default_namespace = lambda m: any, ns: str -> any {
    _meta = m.metadata if m.metadata else {}
    m if not ns or _meta.namespace or m.kind in _CLUSTER_SCOPED else m | {metadata = _meta | {namespace = ns}}
}

_stamp_owner = lambda manifest: any, name: str, part_of: str -> any {
    _meta = manifest.metadata if manifest.metadata else {}
    manifest | {metadata = _meta | {labels = labels_lib.owner_labels(_meta.labels if _meta.labels else {}, name, part_of)}}
}

# The Bundle's Gateways as they are RENDERED: as declared, plus the HTTPS
# listener forge adds for OpenBao.public (its host, and a certificate of its
# own). One definition, read by the Gateway, its Certificate and the render
# output, so none of them disagrees about which listeners exist.
_gateways_rendered = lambda b: Bundle -> [any] {
    _plan = _openbao(b).public_plan if b.openbao and b.openbao.public else None
    [g | {listeners = g.listeners + [_public_listener(b.openbao, _plan)]} if (_plan and _plan.add and g.name == b.openbao.public.gateway) else g for g in b.gateways]
}

_public_listener = lambda ob: any, plan: any -> any {
    GatewayListener {
        name = plan.name
        port = 443
        protocol = "HTTPS"
        hostname = ob.public.host
        if plan.tls:
            tls = GatewayTLS {
                cert_issuer = plan.tls.cert_issuer
                secret_name = plan.tls.secret_name
                mode = plan.tls.mode
            }
    }
}

# The Bundle's ingress: Gateways, their Certificates, and the routes on them.
# All of it is rendered into the PRIMARY namespace.
_ingress = lambda b: Bundle, ns: str -> [any] {
    [item for g in _gateways_rendered(b) for item in gw_lib.render_gateway(g, ns)] \
        + [item for g in _gateways_rendered(b) for item in gw_lib.render_certificate(g, ns)] \
        + [item for r in b.http_routes for item in gw_lib.render_http_route(r, b.gateways, ns, b)] \
        + [item for r in b.grpc_routes for item in gw_lib.render_grpc_route(r, b.gateways, ns, b)]
}

# An object with no cluster of its own lands on the PRIMARY target — the same
# rule _raw_manifests applies, for the same reason: the deploy router
# replicates an unattributed object to EVERY cluster of a multi-cluster env.
# Ingress is never env-wide: a Gateway claims one load balancer (often a
# reserved static IP), its Certificate is one ACME order, and a route's
# backend is a Service on one cluster. Unstamped, a Manifests-only secondary
# cluster received a second Gateway on prod's IP and duplicate certificates.
#
# Bundle.config_maps goes through here too. A ConfigMap defaults to the
# primary NAMESPACE, which a secondary cluster need not have, so replicating
# it was an apply failure there, not merely a stray copy (control-plane's
# registry-config, replicated to its daemon cluster). A label the author set
# is kept, which is how one is placed on another cluster.
_place_on_primary = lambda manifest: any, primary: any -> any {
    _meta = manifest.metadata if manifest.metadata else {}
    _labels = _meta.labels if _meta.labels else {}
    manifest if not primary or _labels["forge.dev/cluster"] else _stamp_cluster(manifest, primary.cluster)
}

_stamp_cluster = lambda manifest: any, context: str -> any {
    _meta = manifest.metadata if manifest.metadata else {}
    _labels = _meta.labels if _meta.labels else {}
    manifest | {metadata = _meta | {labels = {k: v for k, v in _labels if k != "forge.dev/cluster"} | {"forge.dev/cluster" = context}}}
}

# One ObservabilityAgent record per cluster that runs a forge workload, when
# the env declares a destination. Go (pkg/deploy.RenderObservabilityAgent)
# expands it into the DaemonSet and the node-local Service.
_obs_agent_records = lambda b: Bundle, part_of: str -> [any] {
    _o = b.observability
    _targets = {t.cluster: t for t in _cluster_targets(b.workloads)} if _obs_on(b) and b.observability.destination else {}
    [{
        apiVersion = "forge.dev/v1alpha1"
        kind = "ObservabilityAgent"
        metadata = {
            name = OTEL_AGENT_SERVICE
            namespace = _o.agent.namespace
            labels = labels_lib.managed_labels(OTEL_AGENT_SERVICE, part_of) | {"forge.dev/cluster" = t.cluster}
        }
        spec = {
            "endpoint": _o.destination.endpoint
            "protocol": _o.destination.protocol
            "headersSecret": {
                "name": _o.destination.headers_secret.name
                "key": _o.destination.headers_secret.key
            } if _o.destination.headers_secret else None
            "tokenSecret": {
                "name": _obs_token_secret(_o).name
                "key": _obs_token_secret(_o).key
            } if _obs_token_secret(_o) else None
            "signals": {
                "traces": _o.signals.traces
                "metrics": _o.signals.metrics
                "logs": _o.signals.logs
                "profiles": _obs_profiles_on(b)
            }
            "image": _o.agent.image
            "memoryLimitMiB": _o.agent.memory_limit_mib
        }
    } for _, t in _targets]
}

# DeliveryPolicy, every control resolved. The key set is the release shape's
# `delivery_policy`; lists are sorted so the shape does not depend on the order
# the author typed them in.
_render_delivery_policy = lambda p: DeliveryPolicy -> any {
    _ex = {e.id: {"id": e.id, "reason": e.reason, "expires": e.expires} for e in p.vuln_gate.exemptions}
    {
        "trusted_builder": {"mode": p.trusted_builder.mode, "builders": sorted(p.trusted_builder.builders)}
        "signing": {
            "mode": p.signing.mode
            "key": p.signing.key
            "key_ref": p.signing.key_ref if p.signing.key_ref else None
            "oidc_issuer": p.signing.oidc_issuer if p.signing.oidc_issuer else None
            "oidc_subject": p.signing.oidc_subject if p.signing.oidc_subject else None
        }
        "provenance_sbom": {"mode": p.provenance_sbom.mode}
        "flux_verify": {"mode": p.flux_verify.mode}
        "admission_verify": {"mode": p.admission_verify.mode}
        "signed_promotions": {"mode": p.signed_promotions.mode}
        "vuln_gate": {
            "mode": p.vuln_gate.mode
            "block_severity": p.vuln_gate.block_severity
            "warn_severity": p.vuln_gate.warn_severity
            "block_kev": p.vuln_gate.block_kev
            "fixable_only": p.vuln_gate.fixable_only
            "exemptions": [_ex[i] for i in sorted(_ex)]
        }
        "bundle_kinds": {"mode": p.bundle_kinds.mode, "allow": sorted(p.bundle_kinds.allow), "deny": sorted(p.bundle_kinds.deny)}
        "approvals": {"mode": p.approvals.mode, "required": p.approvals.required, "approvers": sorted(p.approvals.approvers)}
    }
}

# ReleaseHealth, resolved. The key set is the release shape's
# `release_health`; the window is in seconds and fail_on is sorted.
_render_release_health = lambda h: ReleaseHealth -> any {
    {
        "window_seconds": _release_health_seconds(h.window)
        "min_requests": h.min_requests
        "max_error_rate_increase": h.max_error_rate_increase
        "max_p95_ratio": h.max_p95_ratio
        "new_issue_min_events": h.new_issue_min_events
        "fail_on": sorted(h.fail_on)
    }
}

# The declaration, as the Go side (forge env up, the local forwarder) reads it.
_render_observability = lambda b: Bundle -> any {
    _o = b.observability
    {
        "enabled": _o.enabled
        "local_port": _o.local_port
        "local_endpoint": _obs_local_endpoint(b) or None
        "profiles_endpoint": _obs_profiles_local_endpoint(b) or None
        # The local alerting loop (Alertmanager + evaluator): only beside the
        # local ClickStack, which is what the evaluator queries.
        "alertmanager_port": _o.alertmanager_port if _o.enabled and not _o.destination and _o.alertmanager_port > 0 else None
        "alerts_port": _o.alerts_port if _o.enabled and not _o.destination and _o.alerts_port > 0 else None
        "destination": {
            "endpoint": _o.destination.endpoint
            "protocol": _o.destination.protocol
            "headers_secret": {
                "name": _o.destination.headers_secret.name
                "key": _o.destination.headers_secret.key
            } if _o.destination.headers_secret else None
            "token_secret": {
                "name": _obs_token_secret(_o).name
                "key": _obs_token_secret(_o).key
            } if _obs_token_secret(_o) else None
            "token_from_control_plane": _o.destination.token_from_control_plane
        } if _o.destination else None
        "signals": {
            "traces": _o.signals.traces
            "metrics": _o.signals.metrics
            "logs": _o.signals.logs
            "profiles": _obs_profiles_on(b)
            "metadata": _o.signals.metadata
            "browser": _o.signals.browser
            "replay": _o.signals.replay
        }
        # The opt-out (and the one opt-in), resolved: recorded in the release shape.
        "policy": {
            "enabled": _o.enabled
            "disabled": _obs_disabled(b)
            "opt_in": ["replay"] if _o.enabled and _o.signals.replay else []
        }
        "sampling": {
            "traces_ratio": _o.sampling.traces_ratio
            "errors_always": _o.sampling.errors_always
            "destination_tail_samples": _o.sampling.destination_tail_samples
        }
        "retention": {
            "traces_days": _o.retention.traces_days
            "metrics_days": _o.retention.metrics_days
            "logs_days": _o.retention.logs_days
            "replay_days": _o.retention.replay_days
            "errors_days": _o.retention.errors_days
            "product_events_days": _o.retention.product_events_days
        }
        "agent": {
            "namespace": _o.agent.namespace
            "image": _o.agent.image
            "memory_limit_mib": _o.agent.memory_limit_mib
        }
        # Alerting is a declaration only: the author's rules (severity
        # defaulted), the tuning of the generated defaults, and the routes. Go
        # resolves the defaults and, absent routes, the in-app + Slack fan-out.
        if _o.alerts.rules or _o.alerts.defaults or _o.alerts.routes or _o.alerts.receivers:
            "alerts": _render_alerts(_o.alerts)
    }
}

_render_alerts = lambda a: Alerts -> any {
    {
        "rules": [{
            "alert": r.alert
            "expr": r.expr
            "for": r["for"]
            "interval": r.interval
            "keep_firing_for": r.keep_firing_for if r.keep_firing_for else None
            "labels": {"severity": "warning"} | r.labels
            "annotations": r.annotations
            "enabled": r.enabled
        } for r in a.rules]
        "defaults": {id: {
            "enabled": d.enabled
            "labels": d.labels
            "for": d["for"] if d["for"] else None
            "params": d.params
        } for id, d in a.defaults}
        "routes": [{"match": r.match, "receiver": r.receiver, "continue": r.continue} for r in a.routes]
        "receivers": [{
            "name": r.name
            "type": r.type
            "url": r.url if r.url else None
            "secret": r.secret if r.secret else None
            "channel": r.channel if r.channel else None
            "severities": r.severities
            "always_alertnames": r.always_alertnames
        } for r in a.receivers]
    }
}

_render_managed_database_record = lambda db: any, part_of: str -> any {
    {
        apiVersion = "forge.dev/v1alpha1"
        kind = "ManagedDatabase"
        metadata = {
            name = db.name
            namespace = db.namespace
            labels = labels_lib.managed_labels(db.name, part_of) | {"forge.dev/cluster" = db.cluster}
        }
        spec = db.spec
    }
}

# A database's placement: its runtime, else the primary cluster target.
_render_database = lambda db: any, bundle: Bundle -> any {
    _rt = _db_runtime(db)
    _target = (_rt.target if _rt.type == "cluster" else Undefined) if _rt else _primary_target(bundle)
    {
        "name": db.name
        "runtime": "hosted" if _rt and _rt.type == "hosted" else "cluster"
        "cluster": _target.cluster if _target else None
        "namespace": (db.namespace if db.namespace else _target.namespace) if _target else None
        "spec": tiers.managed_database_json(db.spec)
    }
}

# The Namespaces the cluster runtime deploys into, one per (cluster,
# namespace), primary target first. RenderWorkloads does not emit them: the
# namespace belongs to the ENV (restricted PSA, forge.dev/env), not to a
# workload.
#
# EVERY Namespace is stamped with its own cluster, including the primary's —
# the rule _raw_manifests already follows, and for the same reason. The deploy
# router replicates an object carrying no forge.dev/cluster to every cluster
# of a multi-cluster env rather than guessing a primary, so leaving the
# primary's Namespace unstamped sent it to every cluster AND made it collide
# with the stamped Namespace each of those clusters already had. Two
# documents then reached one (cluster, Namespace, name) with different bodies,
# which `kubectl apply` resolves last-wins — silently deciding a namespace's
# pod-security level by document order.
_namespaces = lambda b: Bundle, entities: [any] -> [any] {
    _pt = _primary_target(b)
    _pairs = ([[_pt.cluster, _pt.namespace]] if _pt else []) \
        + [[e.runtime.cluster, e.runtime.namespace] for e in entities if e.runtime.type == "cluster"] \
        + ([[_openbao(b).cluster, _openbao(b).namespace]] if b.openbao else [])
    _uniq = {"${p[0]}/${p[1]}": p for p in _pairs}
    [_render_namespace(p[1], p[0], _pt) for _, p in _uniq]
}

_render_namespace = lambda namespace: str, context: str, primary: any -> any {
    {
        apiVersion = "v1"
        kind = "Namespace"
        metadata = {
            name = namespace
            labels = {
                "app.kubernetes.io/managed-by" = "forge"
                "pod-security.kubernetes.io/enforce" = "restricted"
                "pod-security.kubernetes.io/audit" = "restricted"
                "pod-security.kubernetes.io/warn" = "restricted"
            } | ({"forge.dev/cluster" = context} if context else {})
        }
    }
}

# ---------------------------------------------------------------------------
# forge.OpenBao (lib/openbao.k). Unset renders nothing.
# ---------------------------------------------------------------------------

# The lowering of Bundle.openbao, or Undefined: the OpenBao record, the
# resolved block, and the prerequisite Secrets. The store runs on its own
# `runtime` target, else the env's primary one.
_store_cluster_id = lambda b: Bundle, t: ClusterTarget -> str {
    _wl = [w.runtime.target.connected_cluster for w in b.workloads if w.runtime and w.runtime.type == "cluster" and w.runtime.target.cluster == t.cluster and w.runtime.target.connected_cluster]
    _eso = [e.cluster.connected_cluster for e in b.external_secrets_operators if typeof(e.cluster) == "ClusterTarget" and e.cluster.cluster == t.cluster and e.cluster.connected_cluster]
    (b.connected_clusters[t.cluster] if t.cluster in b.connected_clusters else "") or t.connected_cluster or (_wl[0] if _wl else "") or (_eso[0] if _eso else "") or t.cluster
}

_openbao = lambda b: Bundle -> any {
    _rt = b.openbao.runtime if b.openbao else Undefined
    _target = _rt.target if _rt else _primary_target(b)
    openbao_lib.render(b.openbao, {
        project = b.project
        env = labels_lib.env_value(b.env)
        target = _target
        # The id SF5's SecretStores log in under (lib/external_secret.k
        # _cluster_id, which is private to that module): the mount, role and
        # audience this component grants must be the ones a SecretStore asks
        # for. Pinned together by TestOpenBaoAndExternalSecretAgreeOnTheClusterID.
        cluster_id = _store_cluster_id(b, _target) if _target else ""
        secrets = b.required_secrets
        eso_namespaces = [e.namespace for e in b.external_secrets_operators if _target and _eso_context(e) == _target.cluster]
        bootstrap_source = b.bootstrap_source
        gateways = b.gateways
        ingress_cluster = _primary_target(b).cluster if _primary_target(b) else ""
        ingress_namespace = _primary_target(b).namespace if _primary_target(b) else ""
        lifecycle = b.lifecycle
    }) if b.openbao else Undefined
}

_manifests = lambda b: Bundle, entities: [any] -> [any] {
    _pt = _primary_target(b)
    _ns = _pt.namespace if _pt else ""
    _project = b.project
    _dbs = [_render_database(db, b) for db in b.databases]
    labels_lib.stamp_env_all(
        [_render_runtime_class_manifest(rc, _ns, _project) for rc in b.runtime_classes] \
        + _namespaces(b, entities) \
        + [_place_on_primary(_render_config_map_manifest(cm, _ns, _project), _pt) for cm in b.config_maps] \
        + [_workload_record(e, _project) for e in entities if e.runtime.type == "cluster" and e.kind != "tool"] \
        + _obs_agent_records(b, _project) \
        + [_render_managed_database_record(db, _project) for db in _dbs if db.runtime == "cluster"] \
        + ([_openbao(b).record] if b.openbao else []) \
        + [_place_on_primary(item, _pt) for item in _ingress(b, _ns)] \
        + [item for g in b.manifests for item in _raw_manifests(g, _pt, _project)] \
        + es_lib.manifests(b, _pt),
        b.env,
    )
}

# ---------------------------------------------------------------------------
# The entrypoint.
# ---------------------------------------------------------------------------

render = lambda bundle: Bundle -> any {
    _ok = _check_before(bundle.workloads)
    _built_images_ok = _check_built_images(bundle.workloads)
    _entities = [_lower(w, bundle) for w in bundle.workloads]
    _pt = _primary_target(bundle)
    {
        "project": bundle.project
        "env": bundle.env
        "image_tag": bundle.image_tag
        # WHO APPLIES: "local" | "ephemeral" | null (a real env, reconciled
        # from a bundle). Projected unconditionally so the Go consumer reads
        # a stable key, and null is the meaningful default rather than a
        # missing one.
        "lifecycle": bundle.lifecycle if bundle.lifecycle else None
        # WHO APPLIES, declared: "forge" for a real env forge applies (the
        # platform's own env), else null. See Bundle.applier.
        "applier": bundle.applier if bundle.applier else None
        # WHICH DELIVERY UNITS THE HUB CONVERGES. The key appears only when the
        # Bundle declares it, and an empty list IS a declaration (the hub
        # converges none): absent keeps forge's inference, [] ends it.
        if bundle.hub_converges != Undefined:
            "hub_converges": bundle.hub_converges
        "clusters": [_render_cluster(c) for c in bundle.clusters]
        if _pt:
            "cluster_target": _render_cluster_target(_pt)
        "kubeconfig_secrets": [_render_kubeconfig_secret(k) for k in bundle.kubeconfig_secrets]
        "connected_clusters": bundle.connected_clusters if bundle.connected_clusters else None
        "network_policy": {
            "egress_ports": bundle.network_policy.egress_ports
            # The node agent is the env's collector: the telemetry allow
            # follows it unless the author named another.
            "telemetry_namespace": bundle.network_policy.telemetry_namespace or (bundle.observability.agent.namespace if _obs_on(bundle) and bundle.observability.destination and len(_cluster_targets(bundle.workloads)) > 0 else "")
            "ingress_namespace": bundle.network_policy.ingress_namespace
        } if bundle.network_policy else None
        "workloads": _entities
        "release_manifest_consumers": [{
            "name": g.name
            "cluster": g.cluster or (_pt.cluster if _pt else "")
            "release_inputs": g.release_inputs
            if g.release_dependencies is not Undefined:
                "release_dependencies": g.release_dependencies
        } for g in bundle.manifests if g.name and g.release_inputs]
        if bundle.observability:
            "observability": _render_observability(bundle)
        # The resolved delivery policy, every control spelled out, only when the
        # env declares one. Go records it in the release shape unless it is all
        # defaults.
        if bundle.delivery_policy:
            "delivery_policy": _render_delivery_policy(bundle.delivery_policy)
        # How the env's data may be read (forge db query), resolved: the
        # declaration, else what the lifecycle implies. Present only when it
        # is not "audited", the default of an env with no lifecycle: absence
        # means audited, never open, so a real env's render is unchanged.
        if _data_access_db_query(bundle.data_access, bundle.lifecycle or "") != "audited":
            "data_access": {"db_query": _data_access_db_query(bundle.data_access, bundle.lifecycle or "")}
        # The release-health policy, resolved, only when the env declares one.
        # Go records it in the release shape unless it is all defaults.
        if bundle.release_health:
            "release_health": _render_release_health(bundle.release_health)
        # The env's managed identity as DECLARED, with the applications and
        # consumers derived and the deliveries listed. It is release.Shape's
        # `identity` in the shape's own vocabulary.
        if bundle.identity:
            "identity": _render_identity(bundle)
        "infra": [_render_host_infra(i) for i in bundle.infra]
        "frontends": [_render_frontend(f, bundle) for f in bundle.frontends]
        "databases": [_render_database(db, bundle) for db in bundle.databases]
        "config_maps": [_render_config_map(cm) for cm in bundle.config_maps]
        "gateways": [_render_gateway(g) for g in _gateways_rendered(bundle)]
        "http_routes": [_render_http_route(r) for r in bundle.http_routes]
        "grpc_routes": [_render_grpc_route(r) for r in bundle.grpc_routes]
        "runtime_classes": [_render_runtime_class(rc) for rc in bundle.runtime_classes]
        # Platform deps (helm-as-a-RENDERER): the CLI expands each via `helm
        # template --skip-crds` and folds the result into the apply stream,
        # selected by `--target <name>`. Not in `manifests`.
        "helm_charts": [_render_helm_chart(h) for h in _all_helm_charts(bundle) if h.delivery != "bundle"]
        # Bring-your-own YAML, rendered Go-side at extract time and shipped in
        # the bundle (internal/cluster/byo_yaml.go). Keys appear only when
        # declared, so an env with neither renders exactly as before.
        if [h for h in bundle.helm_charts if h.delivery == "bundle"]:
            "bundled_charts": [_render_helm_chart(h) for h in bundle.helm_charts if h.delivery == "bundle"]
        if bundle.generated:
            "generated": [{
                "name": g.name
                "command": g.command
                "dir": g.dir if g.dir else None
                "cluster": g.cluster if g.cluster else None
                "namespace": g.namespace if g.namespace else None
            } for g in bundle.generated]
        # Declared external PREREQUISITES forge does NOT create: a render-time
        # checklist, and the preflight BLOCK on a required-but-absent Secret.
        "required_secrets": [_render_external_secret(s) for s in bundle.required_secrets] + (_openbao(bundle).required if bundle.openbao else [])
        "required_dns": [_render_dns_record(r) for r in bundle.required_dns]
        "external_secrets_operators": [_render_eso(e) for e in bundle.external_secrets_operators]
        # Where the values the store needs to start live (forge.GCPSecretManager
        # or forge.OutOfBand). A declaration; `forge secret import --to-bootstrap`
        # reads it for the project.
        if bundle.bootstrap_source:
            "bootstrap_source": _render_bootstrap_source(bundle.bootstrap_source)
        # The resolved secret store (forge.OpenBao): where it is and how a client
        # logs in. A declaration only; no value.
        if bundle.openbao:
            "openbao": _openbao(bundle).block
        "control_plane_secrets": [_render_control_plane_secret(s, bundle) for s in _cps_all(bundle.workloads, bundle.observability, _cps_decl(bundle.workloads, bundle.identity, bundle.env, bundle.project, bundle.control_plane_secrets))]
        # WHERE secret values come from. A declaration only: no secret VALUE
        # ever enters this output.
        if bundle.secret_provider:
            "secret_provider": bundle.secret_provider
        # Explicitly-placed Secrets, placement RESOLVED here (own
        # cluster/namespace, else the primary target's) so the CLI applies
        # each exactly where it says. A `from = "file"` key carries only the
        # store KEY; its value is resolved CLI-side.
        "rendered_secrets": [{
            "name": s.name
            "keys": s.keys
            "cluster": s.cluster or _pt.cluster
            "namespace": s.namespace or (_pt.namespace if _pt else None)
        } for s in bundle.rendered_secrets]
        # The control plane this env talks to: endpoint + the env var NAME
        # its credential is read from. Declaration only.
        if bundle.control_plane:
            "control_plane": bundle.control_plane
        # TYPED OVERRIDES of forge's own rendered objects, carried OUT of KCL
        # for the Go half to apply. They are not applied here and cannot be:
        # the objects most worth overriding (Deployment, Service) do not exist
        # in `manifests` yet — forge's Workload records expand into them in Go,
        # after this render. So KCL's job is to type the surface, check the key
        # grammar and hand the patches on; `internal/cluster` resolves each key
        # against the EXPANDED stream and merges.
        "overrides": bundle.overrides
        # The delivery units and their order, for the bundle's trees. Keys
        # appear only when declared, so an env without units renders as before.
        if bundle.delivery_units:
            "delivery_units": [{"name": u.name, "after": u.after if u.after != Undefined else None} for u in bundle.delivery_units]
        # The pre-rollout StatefulSet volume-resize Job's tuning; the key
        # appears only when declared. The Job itself is rendered by Go.
        if bundle.statefulset_resize:
            "statefulset_resize": {
                "image": bundle.statefulset_resize.image
                "disabled": bundle.statefulset_resize.disabled
            }
        "manifests": _manifests(bundle, _entities)
    }
}

# ---------------------------------------------------------------------------
# Projections of the declarations the lowering does not transform. Each
# returns a fully-populated dict so the Go consumer sees stable keys.
# ---------------------------------------------------------------------------

_render_host_infra = lambda d: HostInfra -> any {
    {
        "name": d.name
        "engine": d.engine
        "port": d.port
        "database": d.database
        "user": d.user
        "password": d.password
        "data_dir": d.data_dir if d.data_dir else None
        "version": d.version if d.version else None
        # engine = "zitadel" only: where the dev IdP keeps its state, and the
        # declarative bootstrap it converges to. Projected unconditionally:
        # this lambda is a whitelist, so a field omitted here never reaches
        # the consumer however correctly the env declares it.
        "idp_database": d.idp_database
        "idp_database_port": d.idp_database_port if d.idp_database_port else None
        "idp_masterkey": d.idp_masterkey
        "idp_steps_file": d.idp_steps_file
        "idp_pat_path": d.idp_pat_path
        # engine = "nats" / "temporal" only, projected for the same reason.
        "nats_monitor_port": d.nats_monitor_port if d.nats_monitor_port else None
        "temporal_namespaces": d.temporal_namespaces if d.temporal_namespaces else None
        "temporal_ui_port": d.temporal_ui_port if d.temporal_ui_port else None
    }
}

# Build union render. One branch per discriminator value, each a
# fully-populated dict (WorkloadEntity.Build). null = forge builds nothing.
_render_build = lambda b: any -> any {
    (_render_go_build(b) if b.type == "go" else \
        (_render_docker_build(b) if b.type == "docker" else \
        (_render_shell_build(b) if b.type == "shell" else _render_remote_build(b)))) if b else None
}

_render_cluster_target = lambda ct: ClusterTarget -> any {
    {
        "cluster": ct.cluster
        "namespace": ct.namespace
        "connected_cluster": ct.connected_cluster if ct.connected_cluster else None
        "domain": ct.domain if ct.domain else None
        "platform": ct.platform if ct.platform else None
        "image_pull_secrets": ct.image_pull_secrets if ct.image_pull_secrets else []
    }
}

# ---------------------------------------------------------------------------
# Frontends. The runtime is PER FRONTEND (ADR 0002 §6): an OnHosted frontend
# is published to the control plane, which writes its config.js and resolves
# its references; every other frontend's references are resolved here.
# ---------------------------------------------------------------------------

_frontend_hosted = lambda f: Frontend -> bool {
    bool(f.runtime and f.runtime.type == "hosted")
}

# The frontend's static output dir: declared, else the type's convention
# (a Next.js static export writes out/, Vite and Expo web write dist/).
_public_dir = lambda f: Frontend -> str {
    f.public_dir if f.public_dir else ("out" if f.type == "nextjs" else "dist")
}

# The build's content-hashed asset directory, relative to public_dir:
# declared, else the type's convention. "" for a build with none (rn).
_asset_dir = lambda f: Frontend -> str {
    f.asset_dir if f.asset_dir else ({"vite": "assets", "nextjs": "_next/static"}[f.type] if f.type in ["vite", "nextjs"] else "")
}

# The production build shape of each frontend the PROJECT declares, by name:
# "static" | "standalone" | "server" — read from the frontend's own
# next.config (a Vite or Expo build is always "static").
#
# WHY IT IS AN OPTION AND NOT A Frontend FIELD. Whether the build is a static
# export is a fact about the frontend's CODE — its next.config, its routes —
# and it is the same in every env, because one build is promoted through all
# of them. forge.yaml already declares it, and it is what generates the
# next.config and the Dockerfile. A per-env KCL field would be a second
# declaration of that fact, free to disagree with the first in exactly the
# env nobody is looking at. So forge projects the one declaration into every
# render it drives (`-D frontend_outputs='{"web":"standalone"}'`, bound by
# internal/cli renderKCLRaw), and the env states only where the build goes.
#
# Unbound — a plain `kcl run`, or a frontend forge.yaml does not declare (a
# cross-repo `source` frontend) — reads as "": unknown, and nothing is refused
# on a guess.
_frontend_outputs = lambda -> {str:str} {
    _raw = option("frontend_outputs") or ""
    json.decode(_raw) if _raw else {}
}

# The runtimes that SERVE FILES: each publishes the frontend's public_dir and
# runs nothing behind it.
_STATIC_FRONTEND_RUNTIMES = {
    "hosted" = "forge.OnHosted"
    "bucket" = "forge.OnBucket"
    "firebase" = "forge.OnFirebase"
}

# A frontend bound to a static runtime whose build is NOT a static export.
#
# Without this the binding renders, deploys and builds green: `npm run build`
# produces a Node server under .next-prod/, nothing writes public_dir, and the
# failure arrives at publish time as a missing directory — or, for a project
# that once exported, as a stale out/ shipped as the new release. Refused HERE
# because render is where the author is still looking at the binding, and the
# fix is either one forge.yaml line plus a page migration, or a different
# binding — both named.
_not_static_export = lambda f: Frontend -> [str] {
    _t = f.runtime.type
    _outs = _frontend_outputs()
    _out = _outs[f.name] if f.name in _outs else ""
    _env = option("env") or ""
    _where = " in env '${_env}'" if _env else ""
    _dir = f.path if f.path else "frontends/${f.name}"
    [
        "output '${_out}': its production build is not a static export. its next.config builds `output: ${_out}` for frontend '${f.name}' — a Node.js server — and ${_STATIC_FRONTEND_RUNTIMES[_t]} serves only the files a static export writes to public_dir '${_public_dir(f)}', so${_where} nothing would be published. Fix: make the production build in frontends/${f.name}'s next.config a static export (`output: \"export\"`) and migrate what a static export cannot serve — `forge lint --static-export` lists each construct with its file:line and fix — or bind '${f.name}' elsewhere${_where}: a Next.js server is a process, so it ships as a workload (fw.Workload with build = forge.DockerBuild {dockerfile = \"${_dir}/Dockerfile\"})"
    ] if _t in _STATIC_FRONTEND_RUNTIMES and _out and _out != "static" else []
}

# What a frontend's runtime cannot honour, and why. OnHost and OnHosted are
# the workload schemas, so they carry workload-only fields a frontend has no
# use for: a declared one is REFUSED, naming it, rather than silently ignored.
# The static build facts are refused where nothing consumes them.
_frontend_violations = lambda f: Frontend -> [str] {
    _rt = f.runtime
    _t = _rt.type
    _host = _t == "host"
    (["runner '${_rt.runner}': a frontend's dev server is `<dev_runner> dev`; set Frontend.dev_runner, not OnHost.runner"] if _host and _rt.runner != "go-run" else []) \
        + (["air_config: a frontend's dev server has no air config"] if _host and _rt.air_config else []) \
        + (["working_dir: a frontend's dev server runs in its own path"] if _host and _rt.working_dir else []) \
        + (["listen_ports: a frontend's dev server binds Frontend.port; set that"] if _host and _rt.listen_ports is not Undefined else []) \
        + (["delve_port: a frontend's dev server is not a Go process"] if _host and _rt.delve_port != 2345 else []) \
        + (["build_variants: a frontend has one build"] if _t == "build-only" and _rt.build_variants else []) \
        + (["bundle: the dev server assembles nothing; bundle dirs are assembled into a PUBLISHED site"] if _host and f.bundle else []) \
        + (["cache_control: forge sets Cache-Control only where it publishes the build itself (forge.OnBucket object headers, forge.OnFirebase firebase.json headers); ${'the platform owns the CDN and its headers' if _t == 'hosted' else 'nothing here serves the build'}"] \
            if f.cache_control and _t not in ["bucket", "firebase"] else []) \
        + (["image: nothing here pushes a registry artifact — ${'your bucket serves the build' if _t == 'bucket' else ('your Firebase site serves it' if _t == 'firebase' else ('the dev server serves it' if _host else 'the build is not published'))}"] \
            if f.image and _t != "hosted" else []) \
        + (["image is REQUIRED on forge.OnHosted: the site release is pushed to a registry, and the registry is part of the reference (e.g. image = \"ghcr.io/<owner>/${f.name}\"). forge appends the platform's own static.v1 layout to it. There is no env-level registry"] \
            if _t == "hosted" and not f.image else []) \
        + _not_static_export(f) \
        # A BARE image is accepted on forge.OnHosted, exactly as it is on a
        # hosted workload (ADR-0003 F1, _check_image_names_registry): forge
        # resolves it against the base the env DECLARES
        # (<registry_host>/<org>/<project>) and appends the
        # platform's static.v1 layout to the result. The image is still
        # REQUIRED — the release is pushed to a registry and keyed by its
        # repository — but its host is the platform's to supply.
}

# A frontend's runtime_config as declared, with the browser opt-out applied:
# when browser telemetry is off the SDK's ingest path is forced to "off", over
# anything the author set, because the opt-out is the author's explicit word.
_runtime_config_declared = lambda f: Frontend, bundle: Bundle -> {str:str | WorkloadURL} {
    # What forge derives from the frontend's own declarations comes FIRST, so
    # an author's runtime_config entry for the same key always wins.
    _merged = _telemetry_runtime_config(f, bundle) | f.runtime_config
    (_merged | {OTEL_ENDPOINT = "off"}) if _obs_browser_off(bundle) else _merged
}

# The ingest path the platform answers on a hosted site's own origin.
_HOSTED_OTEL_PATH = "/_otel"

# True when the author already decided where this frontend's telemetry goes:
# a runtime_config entry, the typed config's otel_endpoint, or the build
# variable. The runtime config outranks the build variable in the browser, so
# deriving a default over any of these would override the author.
_otel_endpoint_declared = lambda f: Frontend -> bool {
    "OTEL_ENDPOINT" in f.runtime_config or bool(f.config and f.config.otel_endpoint) \
        or any e in f.env_vars { e.name == _otel_endpoint_var(f) }
}

# The runtime config keys derived from `telemetry` and from where the
# frontend runs. They reach the browser as window.__FORGE_CONFIG__ (a hosted
# frontend's through the StaticSite spec, which the control plane writes), so
# one build promotes across envs and each env sets its own.
#
#   OTEL_ENDPOINT        a hosted frontend defaults to the platform's /_otel
#                        unless the author named one (or browser telemetry is
#                        off, which _runtime_config_declared handles)
#   CONSOLE_CAPTURE      "false" only; unset means capture
#   REPLAY_SAMPLE_RATE   only when it is not every session
_telemetry_runtime_config = lambda f: Frontend, bundle: Bundle -> {str:str} {
    _t = f.telemetry
    ({OTEL_ENDPOINT = _HOSTED_OTEL_PATH} if _frontend_hosted(f) and not _otel_endpoint_declared(f) and not _obs_browser_off(bundle) else {}) \
        | ({CONSOLE_CAPTURE = "false"} if _t and not _t.console_capture else {}) \
        | ({REPLAY_SAMPLE_RATE = str(_t.replay_sample_rate)} if _t and _t.replay_sample_rate != 1.0 else {})
}

# The framework's build/dev variable for the browser ingest path.
_otel_endpoint_var = lambda f: Frontend -> str {
    ("NEXT_PUBLIC_" if f.type == "nextjs" else ("VITE_" if f.type == "vite" else "EXPO_PUBLIC_")) + "OTEL_ENDPOINT"
}

# A frontend's runtime_config as the StaticSite spec's `runtimeConfig` wire
# shape, references KEPT (the hosted lowering). Every name is still checked,
# so a typo fails at load rather than at the operator.
_runtime_config_spec = lambda f: Frontend, bundle: Bundle -> {str:} {
    _names = _env_names(bundle.workloads, bundle.infra, bundle.frontends)
    _rcfg = _runtime_config_declared(f, bundle)
    _bad = [k + " -> " + v.name for k, v in _rcfg if typeof(v) != "str" and v.name not in _names]
    assert len(_bad) == 0, \
        "Frontend '${f.name}'.runtime_config: forge.WorkloadURL names a workload this environment does not declare: " \
        + ", ".join(_bad) + ". Declared workloads: " + ", ".join(_names) + "."
    {k: ({"value": v} if typeof(v) == "str" else {"workloadURL": {"name": _require_hosted_url_target(v.name, "Frontend '" + f.name + "'.runtime_config." + k, bundle) if _frontend_hosted(f) else v.name}}) for k, v in _rcfg}
}

# A frontend's runtime_config RESOLVED to literals — the document forge
# writes itself. Empty for an OnHosted frontend (the control plane writes it).
_runtime_config_resolved = lambda f: Frontend, bundle: Bundle -> {str:str} {
    {} if _frontend_hosted(f) else \
        {k: (v if typeof(v) == "str" else _require_workload_url(v.name, "Frontend '" + f.name + "'.runtime_config." + k, bundle)) for k, v in _runtime_config_declared(f, bundle)}
}

# output.frontends[].runtime: {type, ...} — the dispatch key. Only the
# fields the runtime's consumer reads; optional ones project as null.
_render_frontend_runtime = lambda rt: any -> any {
    {
        "type": "bucket"
        "bucket": rt.bucket
        "cdn": _render_static_site_cdn(rt.cdn) if rt.cdn else None
        # keep_releases projects UNCONDITIONALLY, including its 0 ("retain
        # everything") value. A `if rt.keep_releases else None` would erase
        # exactly the setting that says "never delete", and the consumer
        # would read the absent key as the default 10 and start pruning a
        # bucket the author asked it to leave alone.
        "keep_releases": rt.keep_releases
    } if rt.type == "bucket" else ({
        "type": "firebase"
        "project": rt.project
        "site": rt.site
        "target": rt.target if rt.target else None
        "spa_fallback": rt.spa_fallback if rt.spa_fallback else None
        "rewrites": rt.rewrites if rt.rewrites else []
        "keep_asset_releases": rt.keep_asset_releases
    } if rt.type == "firebase" else {"type": rt.type})
}

_render_frontend = lambda f: Frontend, bundle: Bundle -> any {
    _bad = _frontend_violations(f)
    assert len(_bad) == 0, "frontend '${f.name}' cannot run on the ${f.runtime.type} runtime:\n  - " + "\n  - ".join(_bad)
    # Computed BEFORE the literal: a KCL dict key is in scope for the
    # entries after it, so the "bundle" key would shadow the Bundle.
    _rc = _runtime_config_resolved(f, bundle)
    _rc_spec = _runtime_config_spec(f, bundle)
    {
        "name": f.name
        if f.release_dependencies is not Undefined:
            "release_dependencies": f.release_dependencies
        "type": f.type
        "path": f.path if f.path else None
        "source": _render_git_source(f.source) if f.source else None
        "dev_runner": f.dev_runner
        "port": f.port
        "env_file": f.env_file if f.env_file else None
        "env_vars": [_render_env_var(e) for e in f.env_vars if not (_obs_browser_off(bundle) and e.name == _otel_endpoint_var(f))] \
            + ([_render_env_var(EnvVar {name = _otel_endpoint_var(f), value = "off"})] if _obs_browser_off(bundle) else [])
        "config": _render_frontend_config(f.config) if f.config else None
        "runtime": _render_frontend_runtime(f.runtime)
        # Where an OnHosted frontend's release is pushed: the reference the
        # frontend DECLARES. Go appends the platform's static.v1 layout.
        "image": f.image if f.image else None
        # The static build, on the frontend: every publishing runtime
        # builds and assembles the same way.
        "public_dir": _public_dir(f)
        "asset_dir": _asset_dir(f) if _asset_dir(f) else None
        "base_path": f.base_path if f.base_path else None
        "bundle": [_render_bundle_dir(b) for b in f.bundle]
        "cache_control": [_render_cache_rule(r) for r in f.cache_control]
        # The browser's runtime config, as DECLARED (references kept) and as
        # RESOLVED for a frontend forge serves or publishes itself. The Go
        # side layers `runtime_config` over the typed per-env config for
        # config.js, and an OnHosted frontend publishes `runtime_config_spec`
        # as the StaticSite spec's runtimeConfig.
        "runtime_config": _rc
        "runtime_config_spec": _rc_spec
    } | ({"routes": f.routes} if f.routes else {}) \
      | ({"telemetry": _render_browser_telemetry(f.telemetry)} if f.telemetry else {}) \
      | ({"sourcemaps": f.telemetry.sourcemaps} if f.telemetry and f.telemetry.sourcemaps != "publish" else {})
}

# Render an EnvVar to its stable dict form. Unset / empty-string
# defaults project as `null` so the consumer sees stable keys — EXCEPT
# `value`, which preserves an explicitly-set empty string (rendered as
# `""`, not `null`). Empty-string `value` is a legal disable sentinel
# downstream (e.g. RUNTIME_CLASS_NAME="" to skip runtimeClassName).
_render_env_var = lambda ev: EnvVar -> any {
    {
        "name": ev.name
        "value": ev.value if ev.value != Undefined else None
        "secret_ref": ev.secret_ref if ev.secret_ref else None
        "secret_key": ev.secret_key if ev.secret_key else None
        "config_map_ref": ev.config_map_ref if ev.config_map_ref else None
        "config_map_key": ev.config_map_key if ev.config_map_key else None
        # Carried for the SECRET PRE-FLIGHT, which reads these entities and
        # has no proto in hand. Emitted only when true so the JSON contract is
        # unchanged for every var that does not set it. Without this the
        # annotation dies here — the flag survives proto -> codegen -> KCL and
        # then silently fails to reach the one consumer that acts on it.
        "secret_optional": True if ev.secret_optional else None
    }
}


_render_build_variant = lambda bv: BuildVariant -> any {
    {
        "name": bv.name
        "ldflags": bv.ldflags
        "tags": bv.tags
        "goos": bv.goos if bv.goos else None
        "goarch": bv.goarch if bv.goarch else None
    }
}


_render_go_build = lambda b: GoBuild -> any {
    {
        "type": "go"
        "output_name": b.output_name if b.output_name else None
        "unit": b.unit if b.unit else None
        "inputs_file": b.inputs_file if b.inputs_file else None
        "cmd": b.cmd
        "goos": b.goos if b.goos else None
        "goarch": b.goarch if b.goarch else None
        "ldflags": b.ldflags
        "tags": b.tags
        "flags": b.flags
        "env": b.env if b.env else {}
    }
}

_render_docker_build = lambda b: DockerBuild -> any {
    {
        "type": "docker"
        "output_name": b.output_name if b.output_name else None
        "unit": b.unit if b.unit else None
        "inputs_file": b.inputs_file if b.inputs_file else None
        "dockerfile": b.dockerfile
        # The MAIN build context (a project-root-relative dir). None is the
        # project root, which is how every build behaved before this existed.
        "context": b.context if b.context else None
        "platform": b.platform if b.platform else None
        "target": b.target if b.target else None
        "build_args": b.build_args if b.build_args else {}
        # Named build contexts; empty falls back to forge.yaml's
        # docker.build_contexts. Where the image goes is the workload's
        # `image`, not a field here.
        "build_contexts": b.build_contexts if b.build_contexts else {}
    }
}

_render_shell_build = lambda b: ShellBuild -> any {
    {
        "type": "shell"
        "output_name": b.output_name if b.output_name else None
        "unit": b.unit if b.unit else None
        "inputs_file": b.inputs_file if b.inputs_file else None
        "cmd": b.cmd
        "cwd": b.cwd if b.cwd else None
        if b.source:
            "source": {"repo": b.source.repo, "ref": b.source.ref, "subdir": b.source.subdir}
        "env": b.env if b.env else {}
        if b.built_images:
            "built_images": b.built_images
    }
}

_render_remote_build = lambda b: RemoteBuild -> any {
    {
        "type": "remote"
        "output_name": b.output_name if b.output_name else None
        "unit": b.unit if b.unit else None
        "inputs_file": b.inputs_file if b.inputs_file else None
        # The source pin is projected as a nested dict rather than
        # flattened, so the Go side decodes the SAME GitSource shape the
        # frontend and image halves already use instead of a second
        # spelling of repo/ref/subdir that could drift from it.
        "source": {
            "repo": b.source.repo
            "ref": b.source.ref
            "subdir": b.source.subdir if b.source.subdir else None
        }
        "dockerfile": b.dockerfile
        "target": b.target if b.target else None
        "platform": b.platform
        "build_args": b.build_args if b.build_args else {}
        "cpu_millicores": b.cpu_millicores
        "memory_bytes": b.memory_bytes
        "cache_gib": b.cache_gib
        "timeout_seconds": b.timeout_seconds
    }
}


_render_bundle_dir = lambda b: BundleDir -> any {
    {
        "src": b.src
        "dest": b.dest
    }
}

_render_cache_rule = lambda r: CacheRule -> any {
    {
        "pattern": r.pattern
        "cache_control": r.cache_control
    }
}

# An absent `cdn` block projects as null, which the Go consumer reads as
# "bucket served directly, invalidate nothing" — distinct from a declared
# block whose `invalidate` happens to be "none" (a CDN exists, the deploy
# deliberately leaves its cache alone).
_render_static_site_cdn = lambda c: StaticSiteCDN -> any {
    {
        "url_map": c.url_map
        "invalidate": c.invalidate
        "extra_invalidate_paths": c.extra_invalidate_paths
    }
}


# Render a FrontendConfig to its stable dict form. Optional string knobs
# project as `null` when unset so the consumer sees stable keys; `mock`
# and `telemetry_disabled` always carry their (defaulted) value. An unset
# `config` block projects as `null` (see _render_frontend).
_render_frontend_config = lambda c: FrontendConfig -> any {
    {
        "api_url": c.api_url if c.api_url else None
        "mock": c.mock
        "otel_endpoint": c.otel_endpoint if c.otel_endpoint else None
        "environment": c.environment if c.environment else None
        "telemetry_disabled": c.telemetry_disabled
    }
}

# Render a BrowserTelemetry to its stable dict form. An unset `telemetry` is
# omitted from the frontend (see _render_frontend), so a frontend that declares
# none renders exactly as it did before the field.
_render_browser_telemetry = lambda t: BrowserTelemetry -> any {
    {
        "console_capture": t.console_capture
        "replay_sample_rate": t.replay_sample_rate
        "sourcemaps": t.sourcemaps
        "errors": {"ignore": t.errors.ignore}
        "funnels": [{
            "id": f.id
            "title": f.title if f.title else None
            "steps": f.steps
            "window": f.window
            "by": f.by
        } for f in t.funnels]
    }
}

# Render a GitSource to its stable dict form. An unset `source` projects
# as `null` (see _render_frontend), so a path-declared frontend renders
# byte-identically to what it did before cross-repo sources existed.
_render_git_source = lambda s: GitSource -> any {
    {
        "repo": s.repo
        "ref": s.ref
        "subdir": s.subdir if s.subdir else None
    }
}


_render_gateway_listener = lambda l: GatewayListener -> any {
    {
        "name": l.name
        "port": l.port
        "protocol": l.protocol
        "path_prefix": l.path_prefix if l.path_prefix else None
        # Per-listener hostname / TLS. Null means "inherit the gateway's"
        # — consumers (dev urls, doctor, graph) apply the same fallback
        # the renderer does rather than seeing a pre-resolved value, so
        # the JSON stays a faithful projection of what was declared.
        "hostname": l.hostname if l.hostname else None
        "tls": _render_gateway_tls(l.tls) if l.tls else None
    }
}

_render_gateway_tls = lambda t: GatewayTLS -> any {
    {
        "cert_issuer": t.cert_issuer if t.cert_issuer else None
        "secret_name": t.secret_name if t.secret_name else None
        "certmap": t.certmap if t.certmap else None
        "mode": t.mode
    }
}

_render_gateway_address = lambda a: GatewayAddress -> any {
    {
        "type": a.type
        "value": a.value
    }
}

_render_gateway = lambda g: Gateway -> any {
    {
        "name": g.name
        "gateway_class_name": g.gateway_class_name
        "host": g.host if g.host else None
        "tls": _render_gateway_tls(g.tls) if g.tls else None
        "listeners": [_render_gateway_listener(l) for l in g.listeners]
        "addresses": [_render_gateway_address(a) for a in g.addresses] if g.addresses else None
        # cluster is a forge.Cluster REFERENCE; the machine-facing JSON
        # carries the DENORMALIZED kubectl context (k3d-<name>) the scoper
        # matches against — never the bare name.
        "cluster": g.cluster.context if g.cluster else None
    }
}

# The route JSON projections carry what was DECLARED, not what the
# manifest resolved to: `workload` stays a workload NAME and `port` stays
# null when it was inferred. Consumers that need the resolved backend read
# output.manifests, which is the authority; a pre-resolved copy here would
# be a second one that can disagree.
_render_route_retries = lambda r: RouteRetries -> any {
    {
        "on": r.on
        "attempts": r.attempts
        "per_try_timeout": r.per_try_timeout if r.per_try_timeout != Undefined else None
    }
}

_render_route_health_check = lambda hc: RouteHealthCheck -> any {
    {
        "path": hc.path
        "expected_statuses": hc.expected_statuses if hc.expected_statuses != Undefined else None
        "interval": hc.interval
        "timeout": hc.timeout
        "unhealthy_threshold": hc.unhealthy_threshold
        "healthy_threshold": hc.healthy_threshold
    }
}

_render_route_outlier_detection = lambda od: RouteOutlierDetection -> any {
    {
        "consecutive_5xx": od.consecutive_5xx
        "consecutive_gateway_errors": od.consecutive_gateway_errors
        "interval": od.interval
        "base_ejection_time": od.base_ejection_time
        "max_ejection_percent": od.max_ejection_percent
    }
}

_render_route_traffic = lambda t: RouteTraffic -> any {
    {
        "retries": _render_route_retries(t.retries) if t.retries != Undefined else None
        "timeout": t.timeout if t.timeout != Undefined else None
        "health_check": _render_route_health_check(t.health_check) if t.health_check != Undefined else None
        "outlier_detection": _render_route_outlier_detection(t.outlier_detection) if t.outlier_detection != Undefined else None
    }
}

_render_http_route = lambda r: HTTPRoute -> any {
    {
        "name": r.name
        "gateway": r.gateway
        "listener": r.listener
        "service": r.service if r.service else None
        "workload": r.workload if r.workload else None
        "port": r.port if r.port != Undefined else None
        "host": r.host if r.host else None
        "path": r.path if r.path else None
        "traffic": _render_route_traffic(r.traffic) if r.traffic != Undefined else None
        # forge.Cluster reference -> denormalized kubectl context.
        "cluster": r.cluster.context if r.cluster else None
    }
}

_render_grpc_route = lambda r: GRPCRoute -> any {
    {
        "name": r.name
        "gateway": r.gateway
        "listener": r.listener
        "service": r.service if r.service else None
        "workload": r.workload if r.workload else None
        "port": r.port if r.port != Undefined else None
        "host": r.host if r.host else None
        "path": r.path if r.path else None
        "traffic": _render_route_traffic(r.traffic) if r.traffic != Undefined else None
        # forge.Cluster reference -> denormalized kubectl context.
        "cluster": r.cluster.context if r.cluster else None
    }
}

_render_config_map = lambda cm: ConfigMap -> any {
    {
        "name": cm.name
        "namespace": cm.namespace if cm.namespace else None
        "data": cm.data
        "labels": cm.labels
        "annotations": cm.annotations
    }
}


# RuntimeClass JSON projection — stable shape mirroring the schema.
# `overhead` / `scheduling` pass through as raw dicts (or null when
# unset) so the Go consumer can re-emit them into the k8s manifest
# without per-field translation.
_render_runtime_class = lambda rc: RuntimeClass -> any {
    {
        "name": rc.name
        "handler": rc.handler
        "overhead": rc.overhead if rc.overhead else None
        "scheduling": rc.scheduling if rc.scheduling else None
    }
}


# Render a Cluster to its stable dict form. Optional fields project as
# null (not omitted) so the Go consumer (ClusterEntity) sees stable
# keys; servers/agents always carry their (defaulted) int.
#
# Ownership is the `owner` reference; the render layer projects the
# DERIVED `network` (k3d-<owner.name>, null for an owner cluster) and
# `registry_inherit` (true for a secondary) — the two behaviors the
# stringly `network` + `registry_mirror = "inherit"` pair used to carry.
# `context` (k3d-<name>) is projected too so a Go-side consumer can read
# the kubectl context without re-deriving the prefix.
_render_cluster = lambda c: Cluster -> any {
    {
        "name": c.name
        "context": c.context
        "config": c.config if c.config else None
        "image": c.image
        "network": c.network if c.network else None
        "registry_inherit": c.registry_inherit
        "servers": c.servers
        "agents": c.agents
        "api_port": c.api_port if c.api_port else None
        "ingress": c.ingress if c.ingress else None
        "host_ports": c.host_ports if c.host_ports else None
        # The pod / Service CIDRs, emitted ONLY when declared. A CONDITIONAL
        # KEY, not a `None` value like the fields above: the Go side turns a
        # present key into a `--cluster-cidr` k3s arg, so a projected null
        # would be a new key in every existing cluster's JSON for a capability
        # it never asked for. Unset must be indistinguishable from before.
        if c.cluster_cidr:
            "cluster_cidr": c.cluster_cidr
        if c.service_cidr:
            "service_cidr": c.service_cidr
        # NOTE: `kubeconfig` is deliberately NOT projected. It is a HOST fact
        # resolved per-render for KCL consumers to reference
        # (ClusterClient.host_kubeconfig_path); the Go reconcile that reads
        # this entity already knows the machine's kubeconfig without being
        # told, and projecting it would put a machine-specific absolute path
        # into the rendered contract that `forge env render` prints.
    }
}

# Render a KubeconfigSecret to its stable dict form. The CLI mints the
# kubeconfig + applies the Secret Go-side (the IP is resolved fresh, never
# in KCL); this projection only carries the DECLARATION. Never appears in
# render_manifests — the Secret has a runtime-minted value, not a static one.
# The declared charts plus the one chart each ExternalSecretsOperator lowers
# to (lib/eso.k). Built through HelmChart so its check: block runs.
_all_helm_charts = lambda b: Bundle -> [HelmChart] {
    b.helm_charts + [HelmChart {**eso_lib.eso_chart(e, eso_lib.eso_owner(b.project, b.env), es_lib.admission_defaults(b, _primary_target(b), _eso_context(e)))} for e in b.external_secrets_operators]
}

# Render a HelmChart (platform dependency) to its stable dict form. The
# CLI reads this list and runs `helm template <chart|oci> --version
# <version> --values <values> -n <namespace> --skip-crds` Go-side, stamps
# `app.kubernetes.io/name = name` on every rendered manifest, and folds
# them into the apply stream — so `--target <name>` selects them via the
# existing FilterManifestsByApp axis. `values` passes through verbatim.
# NEVER appears in render_manifests — the expansion is a Go-side shell-out
# (helm), not a KCL render.
_render_helm_chart = lambda h: HelmChart -> any {
    {
        "name": h.name
        "chart": h.chart if h.chart else None
        "repo": h.repo if h.repo else None
        "oci": h.oci if h.oci else None
        "version": h.version
        "namespace": h.namespace
        "values": h.values
        "crds": h.crds if h.crds else None
        # Raw manifests that ride the chart's --target (GatewayClass,
        # ClusterIssuers): applied with the chart, excluded from a bare
        # app deploy. The CLI stamps the chart's app-label and applies
        # them after the chart's controllers.
        "manifests": h.manifests if h.manifests else None
        # cluster is a forge.Cluster REFERENCE or a forge.ClusterTarget; the
        # machine-facing JSON carries the DENORMALIZED kubectl context
        # (k3d-<name>, or the target's own) the apply runs `kubectl --context`
        # with — never the bare name. Null => the env's primary cluster.
        "cluster": _eso_context(h) if h.cluster else None
        # True when `cluster` is a forge.ClusterTarget: the target IS the
        # declaration, so the deploy does not look for it in Bundle.clusters.
        "cluster_target": True if (h.cluster and typeof(h.cluster) == "ClusterTarget") else None
        # Metadata forge writes onto the Namespace it creates for the chart,
        # and whether a re-apply prunes the chart's departed objects.
        "namespace_labels": h.namespace_labels if h.namespace_labels else None
        "namespace_annotations": h.namespace_annotations if h.namespace_annotations else None
        "prune": h.prune
        "patches": h.patches if h.patches else None
    }
}

# Render an ExternalSecret prerequisite to its stable dict form. forge does
# NOT create this Secret — the projection is consumed by the render-time
# checklist + the deploy preflight (which BLOCKS when the declared-required
# Secret/key is absent on the live target). `value_group` projects null when
# unset (a standalone prereq, no byte-match group).
_render_external_secret = lambda s: ExternalSecret -> any {
    {
        "type": "secret"
        "name": s.name
        "namespace": s.namespace
        "keys": s.keys
        "reason": s.reason if s.reason else None
        "value_group": s.value_group if s.value_group else None
        # Why a Secret is still provisioned out of band in an env that has
        # store-backed ones: `forge lint --secrets` holds it to a standard.
        if s.out_of_band_reason:
            "out_of_band_reason": s.out_of_band_reason
        # The store a Secret reads from, for the release shape (store_secrets,
        # generated_secrets, pins). Names and kinds only; a value never appears.
        if s.source:
            "source": _render_store_source(s.source)
    }
}

_render_store_source = lambda src: any -> any {
    {
        "store": {"type": src.store.type, if src.store.type == "gcpsm": "project": src.store.project}
        "data": src.data
        if src.generate:
            "generate": {n: _render_generated(g) for n, g in src.generate}
        if src.pin:
            "pin": src.pin
        if src.immutable:
            "immutable": sorted(src.immutable)
        if src.templates:
            "templates": src.templates
        "creation_policy": src.creation_policy
        if src.secret_type != "Opaque":
            "secret_type": src.secret_type
        if src.labels:
            "labels": src.labels
        if src.shadow:
            "shadow": True
        if src.cluster:
            "cluster": src.cluster
    }
}

_render_generated = lambda g: GeneratedSecret -> any {
    {
        "kind": g.kind
        "bytes": g.bytes
        "encoding": g.encoding
        "prefix": g.prefix
        "length": g.length
        "symbols": g.symbols
        "companion": g.companion
        "common_name": g.common_name
        "valid_days": g.valid_days
    }
}

# The ESO installs, as the shape records them. `connected_cluster` is the name
# `forge cluster connect` registered, when the cluster has one.
_render_bootstrap_source = lambda src: any -> any {
    {"type": src.type, if src.type == "gcpsm": "project": src.project}
}

_render_eso = lambda e: ExternalSecretsOperator -> any {
    {
        "cluster": _eso_context(e)
        "connected_cluster": e.cluster.connected_cluster if typeof(e.cluster) == "ClusterTarget" and e.cluster.connected_cluster else None
        "namespace": e.namespace
        "version": e.version
    }
}

# Render a ControlPlaneSecret: where it lands, what the control plane mints into
# it, and which connected cluster to deliver it through. Never a value.
_render_control_plane_secret = lambda s: ControlPlaneSecret, bundle: Bundle -> any {
    {
        "type": "control_plane_secret"
        "name": s.name
        "namespace": _cps_ns(s)
        "cluster": s.cluster.cluster
        "connected_cluster": _cps_connected(s.cluster.cluster, s.cluster, bundle.workloads, bundle.cluster_target, bundle.connected_clusters) or None
        "kind": s.kind
        "keys": s.keys if s.keys else CONTROL_PLANE_SECRET_KINDS[s.kind]
        "required": s.required if s.required else None
        "reason": s.reason if s.reason else None
        # A store-delivered Secret says so and carries the source ESO reads, so
        # the shape can derive the namespace's read policy. Absent for a push
        # Secret, which keeps every existing record as it was.
        if s.delivery == "store":
            "delivery": "store"
            "source": _render_store_source(es_lib.cps_source(s))
    }
}

# Render a DNSRecord prerequisite. A render-time checklist entry (forge
# can't authoritatively verify external DNS); `target` projects null when
# unset (the expected value is often an ephemeral LB IP unknown at author
# time).
_render_dns_record = lambda r: DNSRecord -> any {
    {
        "host": r.host
        "type": r.type
        "target": r.target if r.target else None
        "reason": r.reason if r.reason else None
    }
}

_render_kubeconfig_secret = lambda k: KubeconfigSecret -> any {
    {
        "name": k.name
        "in_cluster": k.in_cluster
        "target_cluster": k.target_cluster
        "context_name": k.context_name
        "key": k.key
        "namespace": k.namespace if k.namespace else None
        "reachability": k.reachability
        "target_context": k.target_context if k.target_context else None
        "service_account": {
            "name": k.service_account.name
            "namespace": k.service_account.namespace
            "namespaces": k.service_account.namespaces
            "rules": [
                {
                    "api_groups": r.api_groups
                    "resources": r.resources
                    "verbs": r.verbs
                    "resource_names": r.resource_names
                } for r in k.service_account.rules
            ]
        } if k.service_account else None
    }
}

# crd — a minimal apiextensions/v1 CRD object (lib/crd.k), for a
# `forge.Manifests {objects = [...]}` entry.
crd = crd_lib.crd

# flux_chart(values, cluster) — Flux as a version-pinned forge.HelmChart
# (lib/flux.k), for a `Bundle.helm_charts` entry.
#
# Declared by a consumer that needs Flux for its OWN reasons — hosted
# tenants, a reconciler it drives itself — so that `forge cluster up` sees a
# chart already providing Flux and installs no second one. Two Flux installs
# in one cluster collide on the same CRDs and the same `flux-system`.
flux_chart = flux_lib.flux_chart

# flux_placement_delivery_v1 / flux_namespace_bindings — the `delivery-v1` pool
# placement for flux_chart(placement = ...), and the per-namespace RoleBindings
# its default RBAC model (rbac = "namespaced") needs in every namespace that holds
# Flux objects (lib/flux.k).
flux_placement_delivery_v1 = flux_lib.flux_placement_delivery_v1
flux_namespace_bindings = flux_lib.flux_namespace_bindings

# FLUX_CHART_OCI / FLUX_CHART_VERSION / FLUX_NAMESPACE — the pinned chart
# reference, its version and Flux's namespace (lib/flux.k). Re-exported
# because FLUX_CHART_OCI is the SHARED-FLUX DETECTOR: a consumer declaring
# this ref is recognised as providing Flux, by reference rather than by the
# chart's freely-chosen name.
FLUX_CHART_OCI = flux_lib.FLUX_CHART_OCI
FLUX_CHART_VERSION = flux_lib.FLUX_CHART_VERSION
FLUX_NAMESPACE = flux_lib.FLUX_NAMESPACE
FLUX_DEPLOY_SERVICE_ACCOUNT = flux_lib.FLUX_DEPLOY_SERVICE_ACCOUNT
FLUX_RECONCILER_ROLE = flux_lib.FLUX_RECONCILER_ROLE
FLUX_SOURCE_ROLE = flux_lib.FLUX_SOURCE_ROLE
FLUX_PRIORITY_CLASS = flux_lib.FLUX_PRIORITY_CLASS
FLUX_RUNTIME_CLASS_GVISOR = flux_lib.FLUX_RUNTIME_CLASS_GVISOR

# target_arch — the GOARCH forge resolved for this build (lib/build.k).
#
# A ShellBuild's `cmd` is plain KCL that forge runs verbatim, so a command
# that cross-compiles reads the arch HERE rather than expecting forge to
# substitute a `${TARGETARCH}` token into its shell:
#
#     cmd = "docker buildx build --platform=linux/${forge.target_arch()} ."
target_arch = build_lib.target_arch

# test_jwks_manifests — top-level re-export of the lib builder so user
# code calls `forge.test_jwks_manifests(jwks, namespace, public_jwk)`
# inside a `forge.Manifests {objects = ...}` of Bundle.manifests. Emits ConfigMap + static-file Deployment + Service serving
# `/.well-known/jwks.json`.
#
# `public_jwk` is the derived PUBLIC JWK. The caller derives it from the
# ES256 private PEM (read from .env.<env>) via the plugin — the same
# pattern as resolve_port, so the plugin is imported in the USER's file
# and the forge module renders fine under a plugin-less `kcl run`:
#
#     import forge
#     import kcl_plugin.forge as plugin
#     _jwks = forge.TestJWKS { name = "auth-jwks", kid = "k1",
#                              private_key = forge.StoreRef { key = "JWKS_PEM" } }
#     _jwk  = plugin.derive_jwk(option("jwks_pem"), _jwks.kid, _jwks.alg)
#     manifests = forge.test_jwks_manifests(_jwks, "auth", _jwk)
test_jwks_manifests = lambda j: TestJWKS, namespace: str, public_jwk: any -> [any] {
    jwks_lib.test_jwks_manifests(j, namespace, public_jwk)
}

# render_gateway / render_certificate / render_http_route /
# render_grpc_route — top-level re-exports of the lib/gateway builders so
# a consumer can emit a Gateway API manifest on a SPECIFIC cluster
# without hand-reimplementing the ~90 lines of builder logic as raw
# dicts. Same pattern as test_jwks_manifests above: a one-line shim onto
# the internal lib function, exposed on the public `forge` namespace.
#
# Pin the manifest to one cluster in a multi-cluster env by setting
# `cluster` on the entity to the forge.Cluster INSTANCE — the builder
# denormalizes its `.context` and stamps `forge.dev/cluster: k3d-<name>`,
# which forge's deploy-time scoper routes to that cluster only:
#
#     import forge
#     _cp = forge.Cluster { name = "control-plane" }
#     _gw = forge.Gateway {
#         name = "cp-public", cluster = _cp
#         listeners = [forge.GatewayListener { name = "http", port = 80, protocol = "HTTP" }]
#     }
#     manifests = [forge.Manifests {objects = forge.render_gateway(_gw, "my-ns")}]
#
# render_http_route / render_grpc_route take the route plus the list of
# gateways (so the builder can resolve the parent listener + host) and
# the namespace, matching the internal signatures.
#
# `bundle` is OPTIONAL and only needed by a route whose backend is a
# `workload`: resolving one is a statement about where that workload runs
# in THIS env, so it reads the env. A route with a `service` backend needs
# no bundle, which is why this stayed a trailing default rather than a
# required fourth argument — and a workload-backed route called without it
# is refused with that explanation rather than rendering a wrong backend.
render_gateway = lambda gw: Gateway, namespace: str -> [any] {
    gw_lib.render_gateway(gw, namespace)
}

render_certificate = lambda gw: Gateway, namespace: str -> [any] {
    gw_lib.render_certificate(gw, namespace)
}

render_http_route = lambda route: HTTPRoute, gateways: [Gateway], namespace: str, bundle: any = Undefined -> [any] {
    gw_lib.render_http_route(route, gateways, namespace, bundle)
}

render_grpc_route = lambda route: GRPCRoute, gateways: [Gateway], namespace: str, bundle: any = Undefined -> [any] {
    gw_lib.render_grpc_route(route, gateways, namespace, bundle)
}



# Render a RuntimeClass as a node.k8s.io/v1 manifest. Cluster-scoped
# (no namespace). Forge labels every RuntimeClass with the standard
# managed-by/part-of so `kubectl get runtimeclasses -l
# app.kubernetes.io/managed-by=forge` is the canonical "what did
# forge create" query and the manifest-driven sweep can prune ones the
# Bundle no longer declares.
_render_runtime_class_manifest = lambda rc: RuntimeClass, namespace: str, part_of: str = "" -> any {
    {
        apiVersion = "node.k8s.io/v1"
        kind = "RuntimeClass"
        metadata = {
            name = rc.name
            # No `app.kubernetes.io/name`: an env-shared resource belongs to no
            # workload, which is what keeps it out of every `--target` group.
            # `part-of` still names the PROJECT (Bundle.project) so the release
            # selector finds it; it falls back to the namespace — the value
            # this label carried before there was a project to name — so a
            # Bundle that declares none renders byte-identically.
            labels = {
                "app.kubernetes.io/managed-by" = "forge"
                "app.kubernetes.io/part-of" = part_of if part_of else namespace
            }
        }
        handler = rc.handler
        if rc.overhead:
            overhead = rc.overhead
        if rc.scheduling:
            scheduling = rc.scheduling
    }
}

# Render a ConfigMap as a k8s manifest. The metadata.namespace
# defaults to the env-wide namespace when the caller hasn't pinned one
# explicitly on the schema. Placement is _manifests' job: it lands on the
# primary target unless `labels` already names a forge.dev/cluster.
_render_config_map_manifest = lambda cm: ConfigMap, namespace: str, part_of: str = "" -> any {
    {
        apiVersion = "v1"
        kind = "ConfigMap"
        metadata = {
            name = cm.name
            namespace = cm.namespace if cm.namespace else namespace
            # Same env-shared shape as RuntimeClass above: no workload `name`,
            # and `part-of` names the project with the historical namespace as
            # its fallback. User labels still win on key conflict.
            labels = {
                "app.kubernetes.io/managed-by" = "forge"
                "app.kubernetes.io/part-of" = part_of if part_of else namespace
            } | cm.labels
            if cm.annotations:
                annotations = cm.annotations
        }
        data = cm.data
    }
}
