"""
Negative test — temporal_metrics_port must differ from the frontend port.
"""

# expect: HostInfra.temporal_metrics_port must be 1..65535 and differ from port

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    infra = [forge.HostInfra {name = "temporal", engine = "temporal", port = 7233, temporal_metrics_port = 7233}]
})
