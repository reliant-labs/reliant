"""
Negative test — a body that is not a field path is refused.
"""

# expect: LogParse field paths (body, trace_id, span_id, drop) must be dotted paths

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
            logs = forge.LogParse {format = "json", body = "a..b"}
        }]
    }
})
