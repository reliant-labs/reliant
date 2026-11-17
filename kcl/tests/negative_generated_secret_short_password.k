"""
Negative test — a password under 8 characters is refused.
"""

# expect: GeneratedSecret.length must be 8..1024

import forge

_g = forge.GeneratedSecret {kind = "password", length = 4}
_b = forge.Bundle {project = "demo", env = "dev", secret_provider = forge.FileSecrets {generate = {"K" = _g}}}
output = forge.render(_b)
