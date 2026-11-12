"""
Negative test — Profiling.heap_interval is whole minutes or hours, 1m to 24h.

The profiler runs one cycle a minute, so a shorter heap cadence cannot be
honoured, and the runtime would silently raise it. Seconds and compound Go
durations ("1h30m") are refused for the same reason: the value must read as
what the process does.
"""

# expect: Profiling.heap_interval must be whole minutes or hours between 1m and 24h

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/demo"
            build = forge.GoBuild {cmd = "./cmd/demo"}
            profiling = fw.Profiling {heap_interval = "30s"}
            runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
        }
    ]
})
