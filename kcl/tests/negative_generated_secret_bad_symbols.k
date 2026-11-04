"""
Negative test — a symbol outside the allowed alphabet is refused.
"""

# expect: outside the allowed alphabet

import forge

_g = forge.GeneratedSecret {kind = "password", symbols = "$`"}
_b = forge.Bundle {project = "demo", env = "dev", secret_provider = forge.FileSecrets {generate = {"K" = _g}}}
output = forge.render(_b)
