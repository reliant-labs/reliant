"""
Positive test — a health gate renders its mode and the envs it follows,
sorted, and defaults to enforce. An env that declares none renders none: the
order is declared, never inferred.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        health_gate = forge.HealthGate {after = ["staging", "canary"]}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_ungated = forge.render(forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_g = output.delivery_policy.health_gate
assert_defaults_to_enforce = _g.mode == "enforce"
assert_after_sorted = _g.after == ["canary", "staging"]
assert_absent_when_undeclared = "health_gate" not in _ungated.delivery_policy
