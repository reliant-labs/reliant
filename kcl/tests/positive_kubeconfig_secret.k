"""
Positive tests for forge.KubeconfigSecret — a cross-cluster kubeconfig
forge mints fresh each up and stores as a k8s Secret.

KCL only emits the DECLARATION; forge mints the kubeconfig + applies the
Secret Go-side (the IP is resolved fresh, never in KCL). The declaration
rides in the ENTITY output (forge.render) alongside `clusters`, and is
NEVER in the k8s manifest stream (the Secret carries a runtime-minted
value, not a static one).

Exercises:
  * The declaration projects all fields, with the key + reachability
    defaults applied.
  * An explicit reachability + namespace + key project through.
  * No kubeconfig_secrets => empty list (stable key).
  * Never appears in output.manifests.
"""

import forge

_default_reach = forge.KubeconfigSecret {
    name = "workload-kubeconfig"
    in_cluster = "k3d-cp"
    target_cluster = "workload"
    context_name = "workload"
}

_explicit = forge.KubeconfigSecret {
    name = "prod-kubeconfig"
    in_cluster = "k3d-cp"
    target_cluster = "prod-workload"
    context_name = "prod-workload"
    key = "config"
    namespace = "system"
    reachability = "endpoint"
}

_cp = forge.Cluster { name = "cp" }
_target = forge.ClusterTarget {
    cluster = "k3d-cp"
    namespace = "dev"
}
_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    clusters = [
        _cp
        forge.Cluster { name = "workload", owner = _cp }
    ]
    kubeconfig_secrets = [_default_reach, _explicit]
    workloads = []
}

_no_kubeconfig_bundle = forge.Bundle { project = "fixture", workloads = [] }

output = forge.render(_bundle)
output_none = forge.render(_no_kubeconfig_bundle)
manifests = forge.render(_bundle).manifests

# ── Invariants ─────────────────────────────────────────────────────

assert_count = len(output.kubeconfig_secrets) == 2

# Defaults: key="kubeconfig", reachability="in-network", namespace null.
assert_default_key = output.kubeconfig_secrets[0].key == "kubeconfig"
assert_default_reach = output.kubeconfig_secrets[0].reachability == "in-network"
assert_default_ns_null = output.kubeconfig_secrets[0].namespace == None
assert_default_in_cluster = output.kubeconfig_secrets[0].in_cluster == "k3d-cp"
assert_default_target = output.kubeconfig_secrets[0].target_cluster == "workload"
assert_default_context = output.kubeconfig_secrets[0].context_name == "workload"

# Explicit overrides project through.
assert_explicit_key = output.kubeconfig_secrets[1].key == "config"
assert_explicit_ns = output.kubeconfig_secrets[1].namespace == "system"
assert_explicit_reach = output.kubeconfig_secrets[1].reachability == "endpoint"

# No kubeconfig_secrets => stable empty list.
assert_none_empty = output_none.kubeconfig_secrets == []

# The minted Secret is NEVER a rendered manifest — no Secret named after
# a kubeconfig declaration appears in output.manifests.
assert_no_manifest = len([m for m in manifests \
    if m.kind == "Secret" and m.metadata.name == "workload-kubeconfig"]) == 0
