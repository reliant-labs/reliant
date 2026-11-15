"""Host startup dependency projection preserves explicit portless declarations."""
import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        forge.Workload {
            name = "desktop"
            command = ["desktop"]
            runtime = forge.OnHost {
                listen_ports = []
                startup_after = ["web"]
            }
        }
        forge.Workload {name = "legacy", command = ["legacy"], runtime = forge.OnHost {}}
    ]
    frontends = [forge.Frontend {name = "web", path = "frontends/web", port = 3100, runtime = forge.OnHost {}}]
})
assert_dependencies = output.workloads[0].runtime.startup_after == ["web"]
assert_portless = output.workloads[0].runtime.listen_ports == []
assert_legacy = output.workloads[1].runtime.startup_after == []
