"""
Negative test — a writer name that could close the CEL string literal an
admission policy embeds it in is refused at the declaration.
"""

# expect: ESOAdmission.writers
# expect: are not user names

import forge

_t = forge.ClusterTarget {cluster = "gke_x", namespace = "n"}
output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _t, admission = forge.ESOAdmission {writers = ["x' || true || '"]}}]
})
