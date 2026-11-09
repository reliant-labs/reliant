"""
Positive test — a TestJWKS may publish an RS256 key.

RS256 is what the common OIDC providers (Zitadel, Auth0, Okta, Keycloak) sign
with, and a verifier configured for one of them pins it. A test IdP limited to
ES256 cannot stand in for any of them, so an environment that runs an
operator listener against a test IdP needs this.

The public JWK is a literal here (in real use: plugin.derive_jwk(pem, kid,
alg)) so a plugin-less `kcl run` exercises the schema and the document.
"""

import forge

_jwks = forge.TestJWKS {
    name = "operator-jwks"
    kid = "op-1"
    alg = "RS256"
    private_key = forge.StoreRef { key = "OPERATOR_JWKS_PEM" }
}

_public_jwk = {
    kty = "RSA"
    n = "abc"
    e = "AQAB"
    kid = "op-1"
    alg = "RS256"
    use = "sig"
}

manifests = forge.test_jwks_manifests(_jwks, "auth", _public_jwk)
_cm = [m for m in manifests if m.kind == "ConfigMap"][0]

assert_alg = _jwks.alg == "RS256"
assert_count = len(manifests) == 3
assert_doc_rsa = "\"kty\": \"RSA\"" in _cm.data["jwks.json"] or "\"kty\":\"RSA\"" in _cm.data["jwks.json"]
assert_doc_kid = "\"op-1\"" in _cm.data["jwks.json"]
