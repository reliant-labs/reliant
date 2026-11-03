"""
Negative test — the reader service account names a Kubernetes service account.
"""

# expect: reader_service_account

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    bootstrap_source = forge.GCPSecretManager {project = "acme-prod", reader_service_account = "Bad_Name"}
})
