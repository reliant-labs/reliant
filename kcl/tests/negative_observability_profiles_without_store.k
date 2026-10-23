"""
Negative test — profiles forced on with the local ClickStack but no profiles
collector: ClickStack's own collector cannot store them, so they would vanish.
"""

# expect: Observability.signals.profiles = True
# expect: profiles_port

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {local_port = 4318, signals = forge.ObservabilitySignals {profiles = True}}
    workloads = [fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/demo"}, runtime = forge.OnHost {}}]
})
