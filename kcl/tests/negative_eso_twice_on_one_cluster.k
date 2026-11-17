"""
Negative test — two ESO declarations for one cluster are refused.
"""

# expect: installs ESO twice
# expect: one External Secrets Operator

import forge

_t = forge.ClusterTarget {cluster = "gke_x", namespace = "n"}
output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _t}, forge.ExternalSecretsOperator {cluster = _t, namespace = "eso-2"}]
})
