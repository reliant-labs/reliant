"""
Negative test — a COMPOSE-bound workload with a forge `build`.

Compose builds (or pulls) its own image from the compose file; a forge build
declared on the same workload would produce an artifact nothing runs, and
the author would debug a compose container built from different sources.
Refused, naming the workload.
"""

# expect: api
# expect: build

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [fw.Workload {
        name = "api"
        build = forge.GoBuild {cmd = "./cmd/api"}
        runtime = forge.OnCompose {service = "api"}
    }]
})
