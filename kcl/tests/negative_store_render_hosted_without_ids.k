"""
Negative test — a HostedStore without the control plane's org and env ids cannot name its values or its JWT role.
"""

# expect: forge.HostedStore with no `org` / `env_id`
# expect: forge.HostedStore {org =

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = _hub
    control_plane = forge.ControlPlane {}
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "acme-prod", keys = ["password"]
        source = forge.StoreSource {store = forge.HostedStore {}, data = {"password" = "P"}}
    }]
})
