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

_build = forge.GoBuild {cmd = "./cmd/reliant", output_name = "reliant"}

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
api_server = fw.Workload {
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
