"""
Negative test — an interval that is not a duration is refused.
"""

# expect: MetricsScrape interval/timeout must be durations

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        telemetry = [forge.Telemetry {
            service_name = "sys"
            cluster = "k3d-demo"
            selector = {app = "sys"}
            metrics = [forge.MetricsScrape {port = 9100, interval = "half a minute"}]
        }]
    }
})
