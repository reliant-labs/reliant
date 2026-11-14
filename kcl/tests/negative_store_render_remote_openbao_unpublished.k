"""
Negative test — an OpenBaoStore read from a cluster that does not run OpenBao, with the component unpublished, has no address to dial.
"""

# expect: the component is not published there
# expect: openbao.public = forge.OpenBaoPublic

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = _hub
    openbao = forge.OpenBao {runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "gke_other", namespace = "o", platform = "amd64"}}}
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "acme-prod", keys = ["password"]
        source = forge.StoreSource {store = forge.OpenBaoStore {}, data = {"password" = "P"}}
    }]
})
