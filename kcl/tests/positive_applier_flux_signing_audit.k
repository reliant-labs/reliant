"""
Positive test — an `applier = "flux"` env that asks for no more than audit
signing keeps what it asked for; only `enforce` is capped (nothing signs a
self-hosted env's bundles yet).
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "qa"
    cluster_target = _k3d
    applier = "flux"
    delivery_policy = forge.DeliveryPolicy {
        signing = forge.Signing {mode = "audit"}
        flux_verify = forge.Control {mode = "off"}
    }
    workloads = [fw.Workload {name = "api", image = "localhost:5050/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

assert_signing_kept = output.flux_delivery.signing == {requested = "audit", effective = "audit"}
assert_verify_kept = output.flux_delivery.flux_verify == {requested = "off", effective = "off"}
