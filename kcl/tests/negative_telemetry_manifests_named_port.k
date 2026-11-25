"""
Negative test — a raw group declares no ports, so a port name is refused.
"""

# expect: Manifests.telemetry: ports must be numbers

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    manifests = [forge.Manifests {name = "ch", cluster = "k3d-demo", telemetry = [forge.Telemetry {selector = {app = "ch"}, metrics = [forge.MetricsScrape {port = "metrics"}]}]}]
})
