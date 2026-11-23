"""
Negative test — `requires` names a workload the env does not declare. A typo
would leave a workload meant to be skipped running without its dependency.
"""

# expect: Workload.requires names a workload this environment does not declare
# expect: alerts -> clickstak

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    workloads = [
        fw.Workload {name = "clickstack", runtime = forge.OnCompose {optional = True}}
        fw.Workload {
            name = "alerts"
            kind = "worker"
            command = ["forge", "obs", "alerts", "run"]
            requires = ["clickstak"]
            runtime = forge.OnHost {listen_ports = []}
        }
    ]
})
