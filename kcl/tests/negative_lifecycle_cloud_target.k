"""
Negative: an env that declares `lifecycle = "local"` while targeting a GKE
cluster.

`lifecycle` claims forge may apply to this env DIRECTLY, which is only honest
for a cluster forge creates and can delete. A cloud cluster is neither, so
the claim is refused at LOAD time, naming the context.

This is the load-bearing half of the field. Without it, a cloud environment
could opt out of reconciliation — and out of every guarantee the bundle path
provides — by adding one line to its main.k.
"""

# expect: a 'local' env may only target clusters forge creates
# expect: gke_acme_us-central1_prod is not one

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_us-central1_prod"
    namespace = "acme-prod"
    platform = "amd64"
}

_bundle = forge.Bundle {
    project = "fixture"
    lifecycle = "local"
    cluster_target = _prod
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api"
        runtime = forge.OnCluster {target = _prod}
    }]
}

output = forge.render(_bundle)
