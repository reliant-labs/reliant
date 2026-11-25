"""
Negative test — otlp_value resolves only transport facts.
"""

# expect: only the transport facts

import forge
import forge.workloads as fw

_v = forge.otlp_value("service_name", forge.Observability {local_port = 4318})

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    workloads = [fw.Workload {name = "x", image = "docker.io/example/x:1", env = {SVC = _v}, runtime = forge.OnHost {}}]
})
