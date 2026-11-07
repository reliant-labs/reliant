"""
Positive test — every GeneratedSecret kind builds with its own parameters, and
the original FileSecrets random form is untouched.
"""

import forge

_random = forge.GeneratedSecret {prefix = "v1:"}
_password = forge.GeneratedSecret {kind = "password", length = 20, symbols = "!-_."}
_ec = forge.GeneratedSecret {kind = "ec_p256_pkcs8"}
_rsa = forge.GeneratedSecret {kind = "rsa_2048", companion = "RSA_PUB"}
_cert = forge.GeneratedSecret {kind = "ec_p256_selfsigned", companion = "TLS_CERT", common_name = "signing.example.com", valid_days = 365}

assert_random_default = _random.kind == "random" and _random.bytes == 32 and _random.encoding == "base64" and _random.prefix == "v1:"
assert_password = _password.kind == "password" and _password.length == 20
assert_ec = _ec.kind == "ec_p256_pkcs8" and not _ec.companion
assert_rsa = _rsa.companion == "RSA_PUB"
assert_cert = _cert.common_name == "signing.example.com" and _cert.valid_days == 365

_bundle = forge.Bundle {
    project = "fixture"
    env = "dev"
    secret_provider = forge.FileSecrets {generate = {"K" = _random}}
}
output = forge.render(_bundle)
assert_file_secrets_still_random = output.secret_provider.generate["K"].kind == "random"
