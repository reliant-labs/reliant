"""
Negative test — severity mapping keys are OTel severities.
"""

# expect: LogSeverity.mapping keys must be

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
            logs = forge.LogParse {format = "json", severity = forge.LogSeverity {field = "level", mapping = {warning = ["W"]}}}
        }]
    }
})
