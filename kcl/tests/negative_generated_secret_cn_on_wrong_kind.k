"""
Negative test — common_name on another kind is refused.
"""

# expect: takes no common_name or valid_days

import forge

_g = forge.GeneratedSecret {kind = "rsa_2048", companion = "P", common_name = "x"}
_b = forge.Bundle {project = "demo", env = "dev", secret_provider = forge.FileSecrets {generate = {"K" = _g}}}
output = forge.render(_b)
