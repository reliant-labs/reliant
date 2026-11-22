"""
Negative test — a scrape more often than every 10s is refused.
"""

# expect: MetricsScrape.interval floor is 10s

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
            metrics = [forge.MetricsScrape {port = 9100, interval = "5s", timeout = "1s"}]
        }]
    }
})
