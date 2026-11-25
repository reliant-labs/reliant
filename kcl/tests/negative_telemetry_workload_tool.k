"""
Negative test — a tool is never scheduled, so it has no telemetry.
"""

# expect: telemetry is not supported for kind='tool'

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    workloads = [fw.Workload {
        name = "cli", kind = "tool", image = "docker.io/example/cli:1"
        telemetry = forge.Telemetry {logs = forge.LogParse {format = "json"}}
        runtime = forge.BuildOnly {}
    }]
})
