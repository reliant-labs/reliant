"""
Negative test — a store server in the admission allow-list must be https://: a
SecretStore sends a service-account token to it.
"""

# expect: ESOAdmission.servers
# expect: must be https:// URLs

import forge

_t = forge.ClusterTarget {cluster = "gke_x", namespace = "n"}
output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _t, admission = forge.ESOAdmission {servers = ["http://openbao:8200"]}}]
})
