"""
Negative test — a raw group's telemetry needs a selector or address.
"""

# expect: Manifests.telemetry: each entry needs a selector (or an address)

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    manifests = [forge.Manifests {name = "ch", cluster = "k3d-demo", telemetry = [forge.Telemetry {metrics = [forge.MetricsScrape {port = 9363}]}]}]
})
