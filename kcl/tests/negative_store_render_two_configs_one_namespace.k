"""
Negative test — two sources in one namespace that configure one store kind differently would fight over one SecretStore.
"""

# expect: differently configured stores of the same kind
# expect: give the sources the same

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = _hub
    control_plane = forge.ControlPlane {}
    required_secrets = [
        forge.ExternalSecret {name = "a", namespace = "acme-prod", keys = ["k"], source = forge.StoreSource {store = forge.HostedStore {org = "o1", env_id = "e1"}, data = {"k" = "A"}}}
        forge.ExternalSecret {name = "b", namespace = "acme-prod", keys = ["k"], source = forge.StoreSource {store = forge.HostedStore {org = "o2", env_id = "e1"}, data = {"k" = "B"}}}
    ]
})
