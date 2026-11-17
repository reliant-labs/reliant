"""
Negative test — a companion on a one-name kind is refused.
"""

# expect: takes no companion

import forge

_g = forge.GeneratedSecret {kind = "ec_p256_pkcs8", companion = "X"}
_b = forge.Bundle {project = "demo", env = "dev", secret_provider = forge.FileSecrets {generate = {"K" = _g}}}
output = forge.render(_b)
