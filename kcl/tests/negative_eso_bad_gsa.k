"""
Negative test — a malformed Google service account is refused.
"""

# expect: gcp_service_account
# expect: must be a Google service account email

import forge

_t = forge.ClusterTarget {cluster = "gke_x", namespace = "n"}
output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _t, gcp_service_account = "nope"}]
})
