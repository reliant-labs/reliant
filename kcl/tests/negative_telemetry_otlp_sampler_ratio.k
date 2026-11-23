"""
Negative test — a sampler ratio above 1 is refused.
"""

# expect: OtlpPush.sampler_ratio must be in (0, 1]

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
            otlp = forge.OtlpPush {sampler_ratio = 1.5}
        }]
    }
})
