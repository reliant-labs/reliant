"""
Positive test — per-category opt-out. logs and metadata off, the rest on:

  * logs     -> OTEL_LOGS_EXPORTER=none on the app;
  * metadata -> no app-side effect (the platform's node agent collects it), only
                the policy the platform enforces at the source;
  * the policy lists exactly what was switched off, sorted, and leaves the app's
    traces/metrics exporters and OTEL_SDK_DISABLED alone.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}
        signals = forge.ObservabilitySignals {logs = False, metadata = False}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_env = {e.name: e.value for w in output.workloads if w.name == "api" for e in w.spec.env if e.value}
assert_logs_exporter_none = _env.OTEL_LOGS_EXPORTER == "none"
assert_traces_untouched = "OTEL_TRACES_EXPORTER" not in _env
assert_metrics_untouched = "OTEL_METRICS_EXPORTER" not in _env
assert_sdk_not_disabled = "OTEL_SDK_DISABLED" not in _env
assert_endpoint_still_injected = "OTEL_EXPORTER_OTLP_ENDPOINT" in _env
assert_policy_enabled = output.observability.policy.enabled == True
assert_policy_disabled_list = output.observability.policy.disabled == ["logs", "metadata"]
assert_signals_say_so = output.observability.signals.metadata == False and output.observability.signals.logs == False
