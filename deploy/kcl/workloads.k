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

control-plane's deploy/kcl/workloads.k declares the same three processes for
the hosted platform (reliant_api_server, reliant_temporal_worker,
daemon_gateway) under the same names. Until it imports these declarations
(P8b, research/forge-migration/12-p8-devtooling-plan.md) the two are kept in
step by hand.
"""

import forge
import forge.workloads as fw
import config_gen

_build = forge.GoBuild {cmd = "./cmd/reliant", output_name = "reliant"}

# The schema migration, to completion, before every other workload.
migrate = fw.Workload {
    name = "reliant-migrate"
    kind = "job"
    build = _build
    args = ["db", "migrate", "up"]
    before = [fw.BEFORE_ALL]
    config_secrets = ["DATABASE_URL"]
}

# Every Connect service, on one mux (GRPC_PORT), plus /health on HEALTH_PORT.
api_server = fw.Workload {
    name = "reliant-api-server"
    kind = "service"
    build = _build
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
} | config_gen.root_of("api")

# The Temporal worker. Nothing dials it: its only listener is the health server.
temporal_worker = fw.Workload {
    name = "reliant-temporal-worker"
    kind = "worker"
    build = _build
    ports = [fw.Port {name = "health", port = 8081}]
    config_secrets = ["RELIANT_VAULT_KEY"]
} | config_gen.root_of("worker")

# The daemon gateway: the bidi stream every daemon dials (TOOLS_DAEMON_PORT).
daemon_gateway = fw.Workload {
    name = "daemon-gateway"
    kind = "service"
    build = _build
    ports = [fw.Port {name = "grpc", port = 9190, expose = True}]
    serves = ["tools-daemon"]
} | config_gen.root_of("gateway")

ALL: [fw.Workload] = [migrate, api_server, temporal_worker, daemon_gateway]
