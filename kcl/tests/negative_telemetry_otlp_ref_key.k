"""
Negative test — an OtlpRef must name a known fact.
"""

# expect: OtlpRef.key 'endpoint_url' must be one of

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
            otlp = forge.OtlpPush {env = {X_ENDPOINT = forge.OtlpRef {key = "endpoint_url"}}}
        }]
    }
})
