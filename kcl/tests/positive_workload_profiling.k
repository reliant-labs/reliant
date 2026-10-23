"""
Positive test — a workload's `profiling` override renders the variables
forge/pkg/observe reads.

  * unset: nothing is rendered, so the runtime's defaults apply (CPU every
    minute, the live heap every 15 minutes);
  * declared: FORGE_PROFILES lists the selected types in the runtime's
    order and FORGE_PROFILES_HEAP_INTERVAL carries the cadence, on every
    runtime that runs a process (cluster, host, hosted);
  * every type off renders "none";
  * a key the workload's own env sets wins, the other is still derived;
  * a tool runs no process, so it gets nothing.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_on_cluster = forge.OnCluster {target = _k3d}
_go = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}

output = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [
        fw.Workload {name = "plain", image = "localhost:5050/demo", build = _go, args = ["a"], runtime = _on_cluster}
        fw.Workload {name = "defaults", image = "localhost:5050/demo", build = _go, args = ["b"], profiling = fw.Profiling {}, runtime = _on_cluster}
        fw.Workload {name = "debugging", image = "localhost:5050/demo", build = _go, args = ["c"], profiling = forge.Profiling {alloc = True, goroutine = True, mutex = True, block = True, heap_interval = "1m"}, runtime = _on_cluster}
        fw.Workload {name = "silent", image = "localhost:5050/demo", build = _go, args = ["d"], profiling = forge.Profiling {cpu = False, heap = False}, runtime = _on_cluster}
        fw.Workload {name = "own-env", image = "localhost:5050/demo", build = _go, args = ["e"], env = {FORGE_PROFILES = "cpu"}, profiling = forge.Profiling {heap_interval = "2h"}, runtime = _on_cluster}
        fw.Workload {name = "local", build = _go, args = ["f"], profiling = forge.Profiling {cpu = False, alloc = True}, runtime = forge.OnHost {}}
        fw.Workload {name = "hosted", image = "ghcr.io/acme/demo", build = _go, ports = [fw.Port {name = "http", port = 8080, expose = True}], profiling = forge.Profiling {goroutine = True}, runtime = forge.OnHosted {}}
        fw.Workload {name = "cli", kind = "tool", build = _go, image = "localhost:5050/demo", args = ["admin"], profiling = forge.Profiling {alloc = True}, runtime = _on_cluster}
    ]
})

_envof = lambda n: str -> {str:} {
    {e.name: e.value for e in ([w for w in output.workloads if w.name == n][0].spec.env or []) if "value" in e}
}

assert_unset_renders_nothing = "FORGE_PROFILES" not in _envof("plain") and "FORGE_PROFILES_HEAP_INTERVAL" not in _envof("plain")
assert_declared_defaults = _envof("defaults")["FORGE_PROFILES"] == "cpu,heap" and _envof("defaults")["FORGE_PROFILES_HEAP_INTERVAL"] == "15m"
assert_every_type = _envof("debugging")["FORGE_PROFILES"] == "cpu,heap,alloc,goroutine,mutex,block" and _envof("debugging")["FORGE_PROFILES_HEAP_INTERVAL"] == "1m"
assert_all_off_is_none = _envof("silent")["FORGE_PROFILES"] == "none"
assert_own_env_wins = _envof("own-env")["FORGE_PROFILES"] == "cpu" and _envof("own-env")["FORGE_PROFILES_HEAP_INTERVAL"] == "2h"
assert_host = _envof("local")["FORGE_PROFILES"] == "heap,alloc"
assert_hosted = _envof("hosted")["FORGE_PROFILES"] == "cpu,heap,goroutine"
assert_tool_gets_nothing = "FORGE_PROFILES" not in _envof("cli")
