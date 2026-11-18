"""
Negative test — a bad bootstrap secret name is refused.
"""

# expect: BootstrapSecret.name
# expect: must be 1-255

import forge

_b = forge.BootstrapSecret {name = "bad name"}
output = forge.render(forge.Bundle {project = "demo", env = "dev"})
