"""
Negative test — a destination endpoint that is not an absolute http(s) URL.
A bare host exports nowhere and says nothing.
"""

# expect: must be an absolute http(s) OTLP URL

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {destination = forge.ObservabilityDestination {endpoint = "otlp.example.com:4318"}}
})
