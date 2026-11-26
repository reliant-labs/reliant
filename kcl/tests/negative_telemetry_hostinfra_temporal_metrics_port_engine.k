"""
Negative test — temporal_metrics_port is for engine temporal only.
"""

# expect: HostInfra.temporal_metrics_port is for engine 'temporal' only

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    infra = [forge.HostInfra {name = "nats", engine = "nats", port = 4222, temporal_metrics_port = 9090}]
})
