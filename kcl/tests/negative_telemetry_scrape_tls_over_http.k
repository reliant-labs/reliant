"""
Negative test — tls on an http scrape is refused.
"""

# expect: MetricsScrape.tls needs scheme https

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
            metrics = [forge.MetricsScrape {port = 9100, tls = forge.ScrapeTLS {insecure_skip_verify = True}}]
        }]
    }
})
