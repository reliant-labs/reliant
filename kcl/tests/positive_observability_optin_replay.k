"""
Positive test — session replay is OPT-IN. `signals.replay = True` is recorded in
the policy's `opt_in` (the platform drops rrweb events unless it is there); it is
not an opt-out, so `disabled` is untouched. Disabling everything cancels it.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_ws = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
_dest = forge.ObservabilityDestination {endpoint = "https://otlp.example.com"}

_on = forge.render(forge.Bundle {
    project = "demo", workloads = _ws
    observability = forge.Observability {destination = _dest, signals = forge.ObservabilitySignals {replay = True}}
})
_off = forge.render(forge.Bundle {
    project = "demo", workloads = _ws
    observability = forge.Observability {enabled = False, signals = forge.ObservabilitySignals {replay = True}}
})
output = _on

assert_opted_in = _on.observability.policy.opt_in == ["replay"]
assert_not_an_opt_out = _on.observability.policy.disabled == []
assert_disabling_everything_cancels_it = _off.observability.policy.opt_in == []
