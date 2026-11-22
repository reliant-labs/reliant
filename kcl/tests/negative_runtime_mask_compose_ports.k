"""
Negative test — the COMPOSE runtime refuses ports / probes / replicas.

A compose-bound workload's container is defined by the compose file: its
image, healthcheck, ports and scale live there. A `ports` (or `probes`,
`replicas`) on the workload would be silently ignored by compose, so it is
refused, naming the workload and the field. The workload contributes its name,
its runtime and its env (merged into the compose process env).
"""

# expect: postgres
# expect: ports

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [fw.Workload {
        name = "postgres"
        ports = [fw.Port {name = "pg", port = 5432}]
        runtime = forge.OnCompose {service = "postgres"}
    }]
})
