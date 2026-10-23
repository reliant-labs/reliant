"""
Negative test — only base64 and hex are supported encodings.
"""

# expect: GeneratedSecret.encoding must be 'base64' or 'hex'

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    secret_provider = forge.FileSecrets {
        generate = {"K" = forge.GeneratedSecret {encoding = "rot13"}}
    }
})
