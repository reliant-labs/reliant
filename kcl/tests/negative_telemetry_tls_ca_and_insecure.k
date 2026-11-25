"""
Negative test — a CA and skipping verification are exclusive.
"""

# expect: ScrapeTLS: ca and insecure_skip_verify are exclusive

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
            metrics = [forge.MetricsScrape {port = 9100, scheme = "https", tls = forge.ScrapeTLS {insecure_skip_verify = True, ca = forge.SecretRef {name = "ca", key = "ca.crt"}}}]
        }]
    }
})
