"""
Positive test — every rendered Namespace is stamped with its own cluster.

The deploy router replicates an object carrying no `forge.dev/cluster` to
EVERY cluster of a multi-cluster env (it never guesses a primary for an
unattributed object). `_render_namespace` used to omit the label on the
PRIMARY cluster's Namespace, which had two consequences on a env whose
secondary cluster also deploys into the env namespace:

  * the primary's Namespace was applied to every cluster, and
  * it collided with the stamped Namespace that cluster already had, so two
    documents reached one (cluster, Namespace, name) with DIFFERENT bodies.

`kubectl apply` is last-wins, so the surviving pod-security level depended on
document order. `forge env shape` refused the render outright — correctly;
the duplicate was real.

Pinned here: one Namespace per (cluster, namespace), each carrying its own
`forge.dev/cluster`, and no unattributed Namespace left for the router to
replicate.
"""

import forge
import forge.workloads as fw

_primary = forge.ClusterTarget {
    cluster = "gke_example_us-central1_prod"
    namespace = "example-prod"
}

# A second cluster that deploys a workload into the SAME namespace — the
# shape that produced the collision.
_daemon = forge.ClusterTarget {
    cluster = "gke_example_us-central1-a_daemon"
    namespace = "example-prod"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _primary
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/example"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _primary}
        }
        fw.Workload {
            name = "proxy"
            image = "localhost:5050/example"
            ports = [fw.Port {name = "http", port = 8081}]
            runtime = forge.OnCluster {target = _daemon}
        }
    ]
}

_m = forge.render(_bundle).manifests
_key = "forge.dev/cluster"
_namespaces = [m for m in _m if m.kind == "Namespace"]
_labels_of = lambda m: any -> any {
    m.metadata.labels if m.metadata and m.metadata.labels else {}
}

# One Namespace per (cluster, namespace) — two clusters, one namespace name.
assert_two_namespaces = len(_namespaces) == 2

# EVERY Namespace carries its own cluster, the primary's included.
assert_every_namespace_stamped = all m in _namespaces {
    _key in _labels_of(m)
}

assert_primary_stamped = len([
    m for m in _namespaces
    if _labels_of(m)[_key] == "gke_example_us-central1_prod"
]) == 1

assert_secondary_stamped = len([
    m for m in _namespaces
    if _labels_of(m)[_key] == "gke_example_us-central1-a_daemon"
]) == 1

# Nothing is left for the deploy router to replicate, so no two documents can
# reach one (cluster, Namespace, name).
assert_no_unattributed_namespace = len([
    m for m in _namespaces if _key not in _labels_of(m)
]) == 0

# The PSA contract the namespace carries is unchanged by the stamping.
assert_restricted_psa = all m in _namespaces {
    _labels_of(m)["pod-security.kubernetes.io/enforce"] == "restricted"
}
