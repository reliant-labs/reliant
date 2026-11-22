"""
Negative test — the COMPOSE runtime refuses `preStopSeconds`.

How a compose service stops (stop_grace_period, stop_signal) is the compose
file's. A pause declared on the workload would be silently ignored, so it is
refused, naming the workload and the field.
"""

# expect: proxy
# expect: preStopSeconds

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [fw.Workload {
        name = "proxy"
        preStopSeconds = 10
        runtime = forge.OnCompose {service = "proxy"}
    }]
})
