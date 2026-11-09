"""
Positive test — MULTI-CLUSTER placement per workload.

Most workloads ride the env's default target; one binds its OWN
forge.OnCluster {target = _daemon}. Each record carries its own
cluster/namespace and its OWN image reference — the registry is declared on the
image, so two workloads in one env can ride two different registries. Go's ExtractManifests
groups records by (forge.dev/cluster, namespace) and calls RenderWorkloads
once per group — `before`/workloadURL resolve within a group, and a
workload lands in the cluster it names, not the first one found.

Replaces the forge.Service `k8s.cluster` override.
"""

import forge
import forge.workloads as fw

_app = forge.ClusterTarget {
    cluster = "k3d-control-plane"
    namespace = "control-plane-prod"
    image_pull_secrets = ["app-pull"]
}
_daemon = forge.ClusterTarget {
    cluster = "k3d-cp-daemon"
    namespace = "cp-daemon-prod"
    image_pull_secrets = ["daemon-pull"]
}

output = forge.render(forge.Bundle {
    project = "control-plane"
    cluster_target = _app
    workloads = [_w | {runtime = forge.OnCluster {target = _app}} if not _w.runtime else _w for _w in [
        fw.Workload {name = "admin", image = "ghcr.io/reliant-labs/app/control-plane", ports = [fw.Port {name = "http", port = 8090}]}
        fw.Workload {
            name = "workspace-proxy"
            image = "ghcr.io/reliant-labs/daemon/control-plane"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _daemon}
        }
    ]]
})

_rt = {w.name: w.runtime for w in output.workloads}
assert_admin_cluster = _rt["admin"].cluster == "k3d-control-plane" and _rt["admin"].namespace == "control-plane-prod"
assert_proxy_cluster = _rt["workspace-proxy"].cluster == "k3d-cp-daemon" and _rt["workspace-proxy"].namespace == "cp-daemon-prod"
assert_proxy_pull_secrets = _rt["workspace-proxy"].image_pull_secrets == ["daemon-pull"]
assert_admin_pull_secrets = _rt["admin"].image_pull_secrets == ["app-pull"]

_rec = {m.metadata.name: m for m in output.manifests if m.kind == "Workload"}
assert_admin_record_ns = _rec["admin"].metadata.namespace == "control-plane-prod"
assert_admin_record_cluster = _rec["admin"].metadata.labels["forge.dev/cluster"] == "k3d-control-plane"
assert_admin_image = _rec["admin"].spec.image.startswith("ghcr.io/reliant-labs/app/control-plane:")
assert_proxy_record_ns = _rec["workspace-proxy"].metadata.namespace == "cp-daemon-prod"
assert_proxy_record_cluster = _rec["workspace-proxy"].metadata.labels["forge.dev/cluster"] == "k3d-cp-daemon"
assert_proxy_image = _rec["workspace-proxy"].spec.image.startswith("ghcr.io/reliant-labs/daemon/control-plane:")
# The runtime carries NO registry: it is a property of the image, and each
# workload's is its own.
assert_runtime_has_no_registry = all n, r in _rt {"registry" not in r}
# One Namespace per distinct cluster-workload namespace (map §2.2).
_ns = sorted([m.metadata.name for m in output.manifests if m.kind == "Namespace"])
assert_namespace_per_target = _ns == ["control-plane-prod", "cp-daemon-prod"]
