"""
Negative test — a malformed bootstrap project is refused.
"""

# expect: GCPSecretManager.project

import forge

output = forge.render(forge.Bundle {project = "demo", env = "dev", bootstrap_source = forge.GCPSecretManager {project = "X"}})
