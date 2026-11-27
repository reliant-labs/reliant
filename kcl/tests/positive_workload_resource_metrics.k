"""
Positive test — `resource_metrics` renders FORGE_OBSERVE_RESOURCE_METRICS,
the switch forge/pkg/observe reads for the process and host resource
metrics.

  * unset everywhere: nothing is rendered, so the runtime's "auto" applies;
  * the env's ObservabilitySignals.resource_metrics is every workload's
    default, on every runtime that runs a process (cluster, host, hosted);
  * a workload's own `resource_metrics` wins over the env's;
  * the workload's own `env` wins over both;
  * a tool runs no process, so it gets nothing.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_on_cluster = forge.OnCluster {target = _k3d}
_go = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}

_workloads = [
    fw.Workload {name = "plain", image = "localhost:5050/demo", build = _go, args = ["a"], runtime = _on_cluster}
    fw.Workload {name = "quiet", image = "localhost:5050/demo", build = _go, args = ["b"], resource_metrics = "off", runtime = _on_cluster}
    fw.Workload {name = "own-env", image = "localhost:5050/demo", build = _go, args = ["c"], env = {FORGE_OBSERVE_RESOURCE_METRICS = "host"}, resource_metrics = "all", runtime = _on_cluster}
    fw.Workload {name = "local", build = _go, args = ["d"], runtime = forge.OnHost {}}
    fw.Workload {name = "hosted", image = "ghcr.io/acme/demo", build = _go, ports = [fw.Port {name = "http", port = 8080, expose = True}], resource_metrics = "all", runtime = forge.OnHosted {}}
    fw.Workload {name = "cli", kind = "tool", build = _go, image = "localhost:5050/demo", args = ["admin"], resource_metrics = "all", runtime = _on_cluster}
]

unset = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = _workloads
})

with_default = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    observability = forge.Observability {signals = forge.ObservabilitySignals {resource_metrics = "process"}}
    workloads = _workloads
})

_envof = lambda out: any, n: str -> {str:} {
    {e.name: e.value for e in ([w for w in out.workloads if w.name == n][0].spec.env or []) if "value" in e}
}
_mode = lambda out: any, n: str -> str {
    _envof(out, n)["FORGE_OBSERVE_RESOURCE_METRICS"] if "FORGE_OBSERVE_RESOURCE_METRICS" in _envof(out, n) else "-"
}

assert_unset_renders_nothing = _mode(unset, "plain") == "-" and _mode(unset, "local") == "-"
assert_workload_without_env_default = _mode(unset, "quiet") == "off" and _mode(unset, "hosted") == "all"
assert_env_default_reaches_every_runtime = _mode(with_default, "plain") == "process" and _mode(with_default, "local") == "process"
assert_workload_wins_over_env = _mode(with_default, "quiet") == "off" and _mode(with_default, "hosted") == "all"
assert_own_env_wins = _mode(unset, "own-env") == "host" and _mode(with_default, "own-env") == "host"
assert_tool_gets_nothing = _mode(unset, "cli") == "-" and _mode(with_default, "cli") == "-"
