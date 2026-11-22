"""
Negative test — a HostInfra's telemetry targets the instance; an address is refused.
"""

# expect: telemetry targets this instance itself

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    infra = [forge.HostInfra {
        name = "nats", engine = "nats", port = 4222, nats_monitor_port = 8222
        telemetry = forge.Telemetry {address = "10.0.0.1", metrics = [forge.MetricsScrape {port = "monitor"}]}
    }]
})
