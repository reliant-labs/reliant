"""
Negative test — a host process exports to the local collector's loopback
port; with none declared there is nowhere for it to send.
"""

# expect: api
# expect: Bundle.observability.local_port

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}}
    workloads = [fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/demo"}, runtime = forge.OnHost {}}]
})
