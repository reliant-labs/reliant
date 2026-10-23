"""
Negative test — a bootstrap value that is a key pair is refused.
"""

# expect: one secret string

import forge

_b = forge.BootstrapSecret {name = "x", generate = forge.GeneratedSecret {kind = "rsa_2048", companion = "P"}}
output = forge.render(forge.Bundle {project = "demo", env = "dev"})
