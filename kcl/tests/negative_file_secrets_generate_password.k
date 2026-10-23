"""
Negative test — FileSecrets.generate mints random only; a password is refused with the escape named.
"""

# expect: use a kind other than 'random'
# expect: source.generate

import forge

_g = forge.GeneratedSecret {kind = "password"}
_b = forge.Bundle {project = "demo", env = "dev", secret_provider = forge.FileSecrets {generate = {"K" = _g}}}
output = forge.render(_b)
