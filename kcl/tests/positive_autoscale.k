"""
Positive test — forge.Autoscale on a cluster service and a hosted worker.

`autoscale` replaces `replicas` with a range. It is lowered verbatim into the
spec (pkg/deploy renders the HorizontalPodAutoscaler from it), with the CPU
target defaulted to 70 when it is not stated. `forge.Autoscale` and
`fw.Autoscale` are the same generated schema. A workload without it carries
no `autoscale` key at all, so its spec is unchanged.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {
    cluster = "k3d-acme"
    namespace = "acme-dev"
}

output = forge.render(forge.Bundle {
    project = "acme"
    cluster_target = _k3d
    control_plane = forge.ControlPlane {}
    workloads = [
        fw.Workload {
            name = "api"
            image = "ghcr.io/acme/api"
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            autoscale = forge.Autoscale {minReplicas = 2, maxReplicas = 6}
            runtime = forge.OnCluster {target = _k3d}
        }
        fw.Workload {
            name = "indexer"
            kind = "worker"
            image = "ghcr.io/acme/indexer"
            autoscale = fw.Autoscale {minReplicas = 1, maxReplicas = 10, cpuTargetPercent = 50}
            runtime = forge.OnHosted {}
        }
        fw.Workload {
            name = "fixed"
            image = "ghcr.io/acme/fixed"
            replicas = 3
            runtime = forge.OnCluster {target = _k3d}
        }
    ]
})

_w = {w.name: w for w in output.workloads}
assert_cluster_range = _w["api"].spec.autoscale == {minReplicas = 2, maxReplicas = 6, cpuTargetPercent = 70}
assert_hosted_range = _w["indexer"].spec.autoscale == {minReplicas = 1, maxReplicas = 10, cpuTargetPercent = 50}
# replicas stays at its default beside autoscale: the autoscaler owns it.
assert_replicas_default = _w["api"].spec.replicas == 1
assert_fixed_unchanged = "autoscale" not in _w["fixed"].spec and _w["fixed"].spec.replicas == 3

# The cluster record carries the same spec RenderWorkloads expands.
_rec = {m.metadata.name: m for m in output.manifests if m.kind == "Workload"}
assert_record_range = _rec["api"].spec.autoscale == {minReplicas = 2, maxReplicas = 6, cpuTargetPercent = 70}
