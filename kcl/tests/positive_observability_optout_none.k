"""
Positive test — the default is everything ON: no policy is recorded, no exporter
is switched off, no SDK is disabled.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    observability = forge.Observability {destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_env = {e.name: e.value for w in output.workloads if w.name == "api" for e in w.spec.env if e.value}
assert_nothing_disabled = output.observability.policy.disabled == []
assert_policy_enabled = output.observability.policy.enabled == True
# profiles are off in a cluster unless set (existing default), so they are excluded.
assert_no_none_exporters = len([k for k, _v in _env if k.endswith("_EXPORTER") and k != "OTEL_PROFILES_EXPORTER"]) == 0
assert_sdk_on = "OTEL_SDK_DISABLED" not in _env
# replay is the one opt-IN category: off unless asked for.
assert_replay_off_by_default = output.observability.signals.replay == False and output.observability.policy.opt_in == []
