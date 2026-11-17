"""
Positive test — Bundle.applier = "forge": a REAL environment that forge applies
by declaration. The platform's own env is the case: it binds clusters its own
hub could converge, and a hub applying the env that runs the hub is circular.

It renders `output.applier == "forge"` and nothing else about the env changes:
the clusters stay real (no `lifecycle` claim is made about them), and the env
still declares its control plane and its connected clusters.
"""

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_prod"
    namespace = "acme-prod"
    connected_cluster = "acme-prod"
    platform = "amd64"
}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    applier = "forge"
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})

assert_applier_is_projected = output.applier == "forge"
# It is not a lifecycle: the env makes no claim that its clusters are scratch.
assert_no_lifecycle_claim = output.lifecycle == None
# Real clusters stay real: the connected binding is intact.
assert_binding_survives = output.cluster_target.connected_cluster == "acme-prod"
