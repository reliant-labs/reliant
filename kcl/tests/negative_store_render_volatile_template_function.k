"""
Negative test — a template function whose output changes on every sync would rewrite the Secret each refresh.
"""

# expect: which ESO's template engine does not have or which changes on every sync
# expect: source.generate

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = _hub
    openbao = forge.OpenBao {}
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "acme-prod", keys = ["password", "stamp"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"password" = "P"}
            templates = {"stamp" = '{{ .password }}-{{ now | date "2006" }}'}
        }
    }]
})
