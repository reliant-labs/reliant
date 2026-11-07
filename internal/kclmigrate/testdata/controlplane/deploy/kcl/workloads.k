"""What control-plane DEPLOYS — one complete fw.Workload per runnable thing.

TRIMMED COPY of control-plane's real deploy/kcl/workloads.k, kept verbatim in
the shapes this migration reads: the shared `_cp_build`, workloads that declare
a bare `image` beside it, and the sibling-repo workloads that declare an image
with no build of their own.
"""

import forge
import forge.workloads as fw

# ── Shared facts ─────────────────────────────────────────────────────────

# The ONE control-plane binary every control-plane workload runs. Every
# workload declaring this (cmd, output) collapses to ONE go build.
_cp_build = forge.GoBuild {cmd = "./cmd/control-plane", output_name = "control-plane"}

admin_server = fw.Workload {
    name = "admin-server"
    kind = "service"
    build = _cp_build
    image = "control-plane"
    args = ["public-api"]
    ports = [fw.Port {name = "http", port = 8090, expose = True}]
}

admin_api = fw.Workload {
    name = "admin-api"
    kind = "service"
    build = _cp_build
    image = "control-plane"
    args = ["admin-api"]
    ports = [fw.Port {name = "http", port = 8090, expose = True}]
}

control_plane_workers = fw.Workload {
    name = "control-plane-workers"
    kind = "worker"
    build = _cp_build
    image = "control-plane"
    args = ["workers"]
    ports = [fw.Port {name = "http", port = 8092}]
}

workspace_proxy = fw.Workload {
    name = "workspace-proxy"
    kind = "service"
    build = _cp_build
    image = "control-plane"
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
}

workspace_controller = fw.Workload {
    name = "workspace-controller"
    kind = "operator"
    build = _cp_build
    image = "control-plane"
    ports = [fw.Port {name = "http", port = 9191}]
}

control_plane_migrate = fw.Workload {
    name = "control-plane-migrate"
    kind = "job"
    build = _cp_build
    image = "control-plane"
}

# ── sibling reliant workloads (built in ../reliant, no build here) ───────

reliant_api_server = fw.Workload {
    name = "reliant-api-server"
    kind = "service"
    image = "reliant"
    command = ["reliant", "server", "api"]
    ports = [fw.Port {name = "grpc", port = 9090, expose = True}]
}

reliant_temporal_worker = fw.Workload {
    name = "reliant-temporal-worker"
    kind = "worker"
    image = "reliant"
    command = ["reliant", "server", "worker"]
}

# The NATS<->daemon bridge. Its own image in the local envs; the cloud envs run
# the unified `reliant` image with an explicit command.
daemon_gateway = fw.Workload {
    name = "daemon-gateway"
    kind = "service"
    image = "reliant-daemon-gateway"
    ports = [fw.Port {name = "grpc", port = 9190, expose = True}]
}
