"""
Negative test — Observability.telemetry needs a selector or address.
"""

# expect: Observability.telemetry: each entry needs a selector (or an address)

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {local_port = 4318, telemetry = [forge.Telemetry {service_name = "eso", cluster = "k3d-demo", metrics = [forge.MetricsScrape {port = 8080}]}]}
})
