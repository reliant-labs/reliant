"""
Negative test — a cluster context that is not a plain identifier cannot name a JWT auth mount.
"""

# expect: not usable as a store mount id
# expect: connected_clusters

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = _hub
    openbao = forge.OpenBao {}
    connected_clusters = {}
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "acme-prod", keys = ["password"]
        source = forge.StoreSource {store = forge.OpenBaoStore {}, data = {"password" = "P"}, cluster = "arn:aws:eks:us-east-1:1:cluster/x"}
    }]
})
