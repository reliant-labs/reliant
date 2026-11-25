"""
Negative test — a timestamp field must be a field path.
"""

# expect: LogTimestamp.field ' ts' is not a field path

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
            logs = forge.LogParse {format = "json", timestamp = forge.LogTimestamp {field = " ts"}}
        }]
    }
})
