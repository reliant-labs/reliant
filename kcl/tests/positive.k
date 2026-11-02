"""
Positive test — the kitchen sink: one Bundle, every runtime, and the §9.1
`output` contract shape each one projects to.

Each `assert_*` identifier must be true (see internal/templates/
kcl_module_test.go). What this pins is the SHAPE Go decodes
(internal/cli/kcl_render.go WorkloadEntity / RuntimeEntity), so a KCL-side
rename breaks here, loudly, instead of as an empty field in the CLI.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {
    cluster = "k3d-myapp"
    namespace = "myapp-dev"
}

# Host: air, cross-repo working_dir, declared listen port. No env on the
# runtime — env is the workload's own `env`, the same on every runtime.
_admin = fw.Workload {
    name = "admin-server"
    build = forge.GoBuild {cmd = "./cmd/myapp", output_name = "myapp"}
    args = ["admin"]
    env = {LOG_LEVEL = "debug"}
    runtime = forge.OnHost {
        runner = "air"
        air_config = "../reliant/.cloud-dev/air.api-server.toml"
        working_dir = "../reliant"
        listen_ports = [8085]
    }
}

# Cluster: two replicas, two named ports.
_proxy = fw.Workload {
    name = "workspace-proxy"
    build = forge.GoBuild {cmd = "./cmd/myapp", output_name = "myapp"}
    image = "localhost:5050/myapp"
    args = ["proxy"]
    replicas = 2
    ports = [
        fw.Port {name = "http", port = 8080, expose = True}
        fw.Port {name = "grpc", port = 9090}
    ]
    runtime = forge.OnCluster {target = _k3d}
}

# Build-only with variants: built and shipped, never run.
_daemon = fw.Workload {
    name = "reliant-daemon"
    kind = "tool"
    build = forge.GoBuild {cmd = "./cmd/daemon"}
    runtime = forge.BuildOnly {
        build_variants = [
            forge.BuildVariant {name = "dev"}
            forge.BuildVariant {name = "prod", ldflags = ["-X main.Env=prod"]}
        ]
    }
}

# An operator and a cron, each bound to the cluster.
_op = fw.Workload {
    name = "workspace-controller"
    kind = "operator"
    image = "localhost:5050/myapp"
    args = ["controller"]
    crds = ["Workspace"]
    clusterRBAC = [{apiGroups = ["forge.io"], resources = ["workspaces"], verbs = ["*"]}]
}
_cron = fw.Workload {
    name = "billing-sweeper"
    kind = "cron"
    image = "localhost:5050/myapp"
    args = ["cron", "billing-sweep"]
    schedule = "@hourly"
}
_job = fw.Workload {
    name = "db-bootstrap"
    kind = "job"
    image = "localhost:5050/myapp"
    args = ["db", "migrate", "up"]
}

_bundle = forge.Bundle {
    project = "myapp"
    env = "dev"
    workloads = [_w | {runtime = forge.OnCluster {target = _k3d}} if not _w.runtime else _w for _w in [_admin, _proxy, _daemon, _op, _cron, _job]]
    frontends = [forge.Frontend {name = "admin-web", path = "frontends/admin-web", runtime = forge.OnHost {}}]
    config_maps = [forge.ConfigMap {name = "myapp-dev-config", data = {"LOG_LEVEL" = "info"}}]
}

output = forge.render(_bundle)

_w = {w.name: w for w in output.workloads}

# ── Top-level contract keys (§9.1) ──────────────────────────────────
assert_project = output.project == "myapp"
assert_env = output.env == "dev"
assert_image_tag_is_str = typeof(output.image_tag) == "str"
assert_buckets_present = all k in ["workloads", "infra", "frontends", "databases", "config_maps", "gateways", "http_routes", "grpc_routes", "runtime_classes", "helm_charts", "required_secrets", "required_dns", "rendered_secrets", "manifests", "clusters", "kubeconfig_secrets"] {
    k in output
}
# The deleted buckets are GONE, not empty: a Go decoder that still reads
# `services` must fail loudly, not see [].
assert_no_legacy_buckets = all k in ["services", "operators", "cronjobs", "jobs"] {
    k not in output
}

# ── Every workload of every runtime is in output.workloads ─────────
assert_all_workloads = len(output.workloads) == 6
assert_runtime_types = [_w[n].runtime.type for n in ["admin-server", "workspace-proxy", "reliant-daemon", "workspace-controller", "billing-sweeper", "db-bootstrap"]] == \
    ["host", "cluster", "build-only", "cluster", "cluster", "cluster"]
assert_kinds = [_w[n].kind for n in ["admin-server", "workspace-controller", "billing-sweeper", "db-bootstrap", "reliant-daemon"]] == \
    ["service", "operator", "cron", "job", "tool"]

# ── Host runtime block ──────────────────────────────────────────────
_h = _w["admin-server"].runtime
assert_host_runner = _h.runner == "air"
assert_host_air_config = _h.air_config == "../reliant/.cloud-dev/air.api-server.toml"
assert_host_working_dir = _h.working_dir == "../reliant"
assert_host_listen_ports = _h.listen_ports == [8085]
assert_host_delve_default = _h.delve_port == 2345
# No command, no env on the runtime: the host argv is derived in Go from
# build + args, and env is the spec's.
assert_host_runtime_has_no_command = "command" not in _h and "env_vars" not in _h
assert_host_env_in_spec = [e for e in _w["admin-server"].spec.env if e.name == "LOG_LEVEL"][0].value == "debug"
assert_host_args_in_spec = _w["admin-server"].spec.args == ["admin"]
# A host workload runs no image.
assert_host_spec_image_empty = "image" not in _w["admin-server"].spec or _w["admin-server"].spec.image == ""

# ── Cluster runtime block: the target's coordinates, resolved ──────
_c = _w["workspace-proxy"].runtime
assert_cluster_coords = _c.cluster == "k3d-myapp" and _c.namespace == "myapp-dev"
# The runtime block carries no registry: the registry is part of the workload's
# image, which is where it is declared and where the spec resolves it from.
assert_cluster_has_no_registry = "registry" not in _c
assert_cluster_image_is_declared_ref = _w["workspace-proxy"].spec.image.startswith("localhost:5050/myapp:")
assert_cluster_optional_null = _c.domain == None and _c.platform == None
assert_cluster_replicas_in_spec = _w["workspace-proxy"].spec.replicas == 2
assert_cluster_ports_in_spec = [p.name for p in _w["workspace-proxy"].spec.ports] == ["http", "grpc"]
assert_cluster_expose = _w["workspace-proxy"].spec.ports[0].expose == True

# ── Build-only runtime block ────────────────────────────────────────
_b = _w["reliant-daemon"].runtime
assert_build_only_variants = [v.name for v in _b.build_variants] == ["dev", "prod"]
assert_build_only_ldflags = _b.build_variants[1].ldflags == ["-X main.Env=prod"]

# ── Build is typed and explicit on every forge-built workload ──────
assert_build_typed = _w["admin-server"].build.type == "go" and _w["admin-server"].build.cmd == "./cmd/myapp"
assert_output_name = _w["admin-server"].build.output_name == "myapp"
# A workload that names only an image is NOT built here: build is null.
assert_image_only_not_built = _w["workspace-controller"].build == None

# ── Operator / cron / job facts ride the spec ───────────────────────
assert_operator_crds = _w["workspace-controller"].spec.crds == ["Workspace"]
assert_operator_rbac = len(_w["workspace-controller"].spec.clusterRBAC) == 1
assert_cron_schedule = _w["billing-sweeper"].spec.schedule == "@hourly"
assert_job_args = _w["db-bootstrap"].spec.args == ["db", "migrate", "up"]

# ── manifests: a Workload record per CLUSTER workload, nothing else ─
_records = {m.metadata.name: m for m in output.manifests if m.kind == "Workload"}
assert_records_are_cluster_only = sorted([k for k in _records]) == \
    ["billing-sweeper", "db-bootstrap", "workspace-controller", "workspace-proxy"]
assert_no_kcl_expansion = len([m for m in output.manifests if m.kind in ["Deployment", "Service", "CronJob", "Job", "ClusterRole"]]) == 0

# ── Frontend and config map buckets unchanged ───────────────────────
assert_frontends = len(output.frontends) == 1 and output.frontends[0].name == "admin-web"
assert_config_map = output.config_maps[0].data["LOG_LEVEL"] == "info"
