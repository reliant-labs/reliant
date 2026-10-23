"""
Negative test — a non-random kind that sets random-only fields is refused.
"""

# expect: takes no bytes, encoding or prefix

import forge

_g = forge.GeneratedSecret {kind = "password", bytes = 16}
_b = forge.Bundle {project = "demo", env = "dev", secret_provider = forge.FileSecrets {generate = {"K" = _g}}}
output = forge.render(_b)
