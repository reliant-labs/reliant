"""
Negative test — a malformed GCP project id is refused.
"""

# expect: GCPSecretManagerStore.project
# expect: not a Google Cloud project id

import forge

_s = forge.GCPSecretManagerStore {project = "Bad_Project"}
output = forge.render(forge.Bundle {project = "demo", env = "dev"})
