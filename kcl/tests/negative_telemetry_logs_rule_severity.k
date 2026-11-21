"""
Negative test — a severity rule's severity is an OTel severity.
"""

# expect: LogSeverityRule.severity must be one of

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
            logs = forge.LogParse {format = "json", rules = [forge.LogSeverityRule {body_match = "x", severity = "warning"}]}
        }]
    }
})
