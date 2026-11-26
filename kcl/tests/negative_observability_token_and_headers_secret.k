"""
Negative test — both credential forms on one destination.

headers_secret and token_secret both write the Authorization header; the agent
cannot honour two, so the schema refuses the pair.
"""

# expect: both headers_secret and token_secret

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {
            endpoint = "https://otlp.example.com"
            headers_secret = forge.ObservabilitySecretRef {name = "otlp-auth", key = "headers"}
            token_secret = forge.ObservabilitySecretRef {name = "otlp-token", key = "token"}
        }
    }
})
