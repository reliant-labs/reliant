"""
Negative test — a generated secret of zero bytes is empty "key material".
"""

# expect: GeneratedSecret.bytes must be 1..1024

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    secret_provider = forge.FileSecrets {
        generate = {"K" = forge.GeneratedSecret {bytes = 0}}
    }
})
