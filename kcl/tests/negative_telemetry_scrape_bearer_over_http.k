"""
Negative test — a bearer token over plain http is refused.
"""

# expect: MetricsScrape.bearer_token is sent only over https

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
            metrics = [forge.MetricsScrape {port = 9100, bearer_token = forge.SecretRef {name = "s", key = "k"}}]
        }]
    }
})
