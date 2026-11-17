"""
Negative test — Observability.dev_telemetry is "auto", "hosted" or "local".
"""

# expect: Observability.dev_telemetry

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {local_port = 4318, dev_telemetry = "cloud"}
})
