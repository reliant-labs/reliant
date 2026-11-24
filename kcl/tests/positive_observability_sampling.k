"""
Positive test — the trace ratio is HEAD sampling in the SDK. Every workload
that runs a process (host, cluster, hosted) gets
OTEL_TRACES_SAMPLER=parentbased_traceidratio and OTEL_TRACES_SAMPLER_ARG=<ratio>,
so every service follows the root span's decision and a trace is whole or
absent. An author's sampler wins whole (the pair), a ratio of 1 adds nothing
(the SDK default keeps everything), and traces switched off adds none.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_go = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
_dest = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
_ws = [
    fw.Workload {name = "api", image = "localhost:5050/demo", build = _go, args = ["api"], runtime = forge.OnCluster {target = _k3d}}
    fw.Workload {name = "local", build = _go, args = ["local"], runtime = forge.OnHost {}}
    fw.Workload {name = "web", image = "ghcr.io/acme/web", ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnHosted {}}
    fw.Workload {name = "own", build = _go, args = ["own"], env = {OTEL_TRACES_SAMPLER = "always_on"}, runtime = forge.OnHost {}}
]
_render = lambda o: forge.Observability -> any {
    forge.render(forge.Bundle {
        project = "demo"
        control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
        observability = o
        workloads = _ws
    })
}
_env = lambda out: any, name: str -> {str:str} {
    {e.name: e.value for e in ([w for w in out.workloads if w.name == name][0].spec.env or []) if e.value}
}

_half = _render(forge.Observability {local_port = 4318, destination = _dest, sampling = forge.ObservabilitySampling {traces_ratio = 0.5}})
_full = _render(forge.Observability {local_port = 4318, destination = _dest})
_notraces = _render(forge.Observability {local_port = 4318, destination = _dest, signals = forge.ObservabilitySignals {traces = False}, sampling = forge.ObservabilitySampling {traces_ratio = 0.5}})
output = _half

assert_every_runtime_head_samples = all n in ["api", "local", "web"] { _env(_half, n).OTEL_TRACES_SAMPLER == "parentbased_traceidratio" and _env(_half, n).OTEL_TRACES_SAMPLER_ARG == "0.5" }
assert_author_sampler_wins_whole = _env(_half, "own").OTEL_TRACES_SAMPLER == "always_on" and "OTEL_TRACES_SAMPLER_ARG" not in _env(_half, "own")
assert_full_ratio_adds_nothing = "OTEL_TRACES_SAMPLER" not in _env(_full, "api") and "OTEL_TRACES_SAMPLER_ARG" not in _env(_full, "api")
assert_no_sampler_when_traces_off = "OTEL_TRACES_SAMPLER" not in _env(_notraces, "api")
