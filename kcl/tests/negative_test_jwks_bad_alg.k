"""
Negative test — TestJWKS.alg must be an ASYMMETRIC signing algorithm forge
can derive a public JWK for ('ES256' or 'RS256'). A symmetric algorithm has no
public half to publish, so a JWKS for it is meaningless and must be refused.
"""

# expect: TestJWKS.alg must be 'ES256'

import forge

_j = forge.TestJWKS {
    name = "test-jwks"
    kid = "k1"
    alg = "HS256"  # symmetric: nothing to publish
    private_key = forge.StoreRef { key = "TEST_JWKS_PEM" }
}
