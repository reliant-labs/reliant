"""
Negative test — a GCPSecretManagerStore reads as ESO itself, so an ESO with no Google service account cannot.
"""

# expect: has no `gcp_service_account`
# expect: forge.ExternalSecretsOperator.gcp_service_account

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = _hub
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub}]
    required_secrets = [forge.ExternalSecret {
        name = "db", namespace = "acme-prod", keys = ["password"]
        source = forge.StoreSource {store = forge.GCPSecretManagerStore {project = "acme-prod"}, data = {"password" = "p"}}
    }]
})
