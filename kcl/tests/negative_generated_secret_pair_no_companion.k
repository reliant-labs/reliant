"""
Negative test — a two-name kind with no companion is refused.
"""

# expect: needs `companion`

import forge

_g = forge.GeneratedSecret {kind = "rsa_2048"}
_b = forge.Bundle {project = "demo", env = "dev", secret_provider = forge.FileSecrets {generate = {"K" = _g}}}
output = forge.render(_b)
