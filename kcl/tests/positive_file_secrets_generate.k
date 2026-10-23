"""
Positive test — FileSecrets.generate: declare a store key as pure random key
material. The declaration projects into the entity output so the CLI can mint
it; defaults are 32 bytes of base64 with no prefix.
"""

# kcl-args: env=dev

import forge

_bundle = forge.Bundle {
    project = "fixture"
    secret_provider = forge.FileSecrets {
        generate = {
            "RELIANT_VAULT_KEY" = forge.GeneratedSecret {prefix = "v1:"}
            "SESSION_SECRET" = forge.GeneratedSecret {bytes = 16, encoding = "hex"}
        }
    }
}

output = forge.render(_bundle)
_gen = output.secret_provider.generate

assert_vault_defaults = _gen["RELIANT_VAULT_KEY"].bytes == 32 and _gen["RELIANT_VAULT_KEY"].encoding == "base64"
assert_vault_prefix = _gen["RELIANT_VAULT_KEY"].prefix == "v1:"
assert_session_hex = _gen["SESSION_SECRET"].bytes == 16 and _gen["SESSION_SECRET"].encoding == "hex"
