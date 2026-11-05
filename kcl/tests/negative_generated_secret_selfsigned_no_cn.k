"""
Negative test — a self-signed certificate without a subject is refused.
"""

# expect: needs common_name

import forge

_g = forge.GeneratedSecret {kind = "ec_p256_selfsigned", companion = "CERT"}
_b = forge.Bundle {project = "demo", env = "dev", secret_provider = forge.FileSecrets {generate = {"K" = _g}}}
output = forge.render(_b)
