"""
Negative test — an MLStack Install applies to the stack's cluster, so that
cluster is one the env targets: a "local" env may not install onto a cloud
cluster it does not create.
"""

# expect: a 'local' env may only target clusters forge creates
# expect: gke_acme_us-central1_gpu is not one

import forge

_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_gpu = forge.ClusterTarget {cluster = "gke_acme_us-central1_gpu", namespace = "forge-ml"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    lifecycle = "local"
    cluster_target = _k3d
    ml_stacks = [forge.MLStack {
        cluster = _gpu
        node_health = forge.Install {kind = "node-problem-detector", chart = forge.HelmChart {name = "node-problem-detector", repo = "https://charts.deliveryhero.io/", chart = "node-problem-detector", version = "2.3.14", namespace = "forge-ml", delivery = "bundle"}}
    }]
})
