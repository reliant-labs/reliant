"""
Negative test — OtlpPush.signals are traces, metrics, logs.
"""

# expect: OtlpPush.signals: traces, metrics, logs

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
            otlp = forge.OtlpPush {signals = ["profiles"]}
        }]
    }
})
