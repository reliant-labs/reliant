"""
Negative test — token_from_control_plane beside a credential the author
provides. Both would write the Authorization header.
"""

# expect: token_from_control_plane = True supplies the credential itself

import forge

output = forge.render(forge.Bundle {
    project = "acme"
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {
            endpoint = "https://otlp.reliantapi.com"
            token_from_control_plane = True
            headers_secret = forge.ObservabilitySecretRef {name = "otlp-auth", key = "headers"}
        }
    }
})
