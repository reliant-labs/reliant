"""
Negative test — a Google service account that is neither an id nor an email.
"""

# expect: reader_gsa

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    bootstrap_source = forge.GCPSecretManager {project = "acme-prod", reader_gsa = "Not A GSA"}
})
