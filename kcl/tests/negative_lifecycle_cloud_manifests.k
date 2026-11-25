"""
Negative: an "ephemeral" env whose WORKLOADS are all on a forge-created k3d
cluster, but which applies one raw `forge.Manifests` group to a cloud
cluster.

An env is only as local as its LEAST local target. Checking only the
workloads (or only `cluster_target`) would let this env — which really does
write into a GKE cluster on every apply — keep direct apply, which is the
hole the check exists to close. So every placement is checked: a workload's,
the primary target's, a pinned Gateway's or route's, a chart's, a raw
manifest group's, a placed Secret's.
"""

# expect: a 'ephemeral' env may only target clusters forge creates
# expect: gke_acme_us-central1_ci is not one

import forge
import forge.workloads as fw

_local = forge.Cluster {
    name = "ci"
}

_k3d = forge.ClusterTarget {
    cluster = "k3d-ci"
    namespace = "fixture-ci"
}

_namespace = {
    apiVersion = "v1"
    kind = "Namespace"
    metadata = {name = "borrowed"}
}

_bundle = forge.Bundle {
    project = "fixture"
    lifecycle = "ephemeral"
    clusters = [_local]
    cluster_target = _k3d
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5051/api"
        runtime = forge.OnCluster {target = _k3d}
    }]
    manifests = [forge.Manifests {
        cluster = "gke_acme_us-central1_ci"
        objects = [_namespace]
    }]
}

output = forge.render(_bundle)
