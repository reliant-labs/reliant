"""
Negative test — an env that runs a container (here the local ClickStack, a
compose service) has a local collector, so its host processes still need
the loopback port it publishes. Only a container-free env may leave
local_port unset (positive_observability_container_free_host.k).
"""

# expect: api
# expect: Bundle.observability.local_port
# expect: the env's compose collector publishes

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {}
    workloads = [
        fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/demo"}, runtime = forge.OnHost {}}
        fw.Workload {name = "clickstack", runtime = forge.OnCompose {service = "clickstack"}}
    ]
})
