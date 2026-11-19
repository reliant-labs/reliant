"""
Positive test — `signals.browser = False` renders OTEL_ENDPOINT=off into every
frontend's runtime config (spec and resolved) and its build variable, over an
author-set "/_otel", and touches nothing server-side.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
        signals = forge.ObservabilitySignals {browser = False}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
    frontends = [forge.Frontend {
        name = "web", path = "web", type = "vite", port = 5173
        runtime = forge.OnHost {}
        env_vars = [forge.EnvVar {name = "VITE_OTEL_ENDPOINT", value = "/_otel"}]
        runtime_config = {OTEL_ENDPOINT = "/_otel"}
    }]
})

_f = output.frontends[0]
_ev = {e.name: e.value for e in _f.env_vars}
assert_runtime_config_off = _f.runtime_config.OTEL_ENDPOINT == "off"
assert_build_variable_off = _ev.VITE_OTEL_ENDPOINT == "off"
assert_one_build_variable = len([e for e in _f.env_vars if e.name == "VITE_OTEL_ENDPOINT"]) == 1
assert_policy = output.observability.policy.disabled == ["browser"]
_env = {e.name: e.value for w in output.workloads if w.name == "api" for e in w.spec.env if e.value}
assert_server_untouched = "OTEL_SDK_DISABLED" not in _env and "OTEL_LOGS_EXPORTER" not in _env
