"""
Negative test — `length` on kind random is refused.
"""

# expect: takes no length or symbols

import forge

_g = forge.GeneratedSecret {length = 16}
_b = forge.Bundle {project = "demo", env = "dev", secret_provider = forge.FileSecrets {generate = {"K" = _g}}}
output = forge.render(_b)
