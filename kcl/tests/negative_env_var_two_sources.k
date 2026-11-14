"""
Negative test — an EnvVar that sets more than one source must FAIL at
render, not produce a manifest the API server rejects.

k8s rejects an env entry carrying both `value` and `valueFrom` ("may
not be specified when `value` is not empty"), and it rejects it only
SERVER-side: `kubectl apply --dry-run=client` passes, CI goes green,
and the deploy dies against the real cluster. Caught here instead, at
the point the ambiguity is written.
"""

# expect: EnvVar 'API_TOKEN' sets more than one source

import forge

_bad = forge.EnvVar {
    name = "API_TOKEN"
    value = "literal"
    secret_ref = "app-secrets"
    secret_key = "api-token"
}
