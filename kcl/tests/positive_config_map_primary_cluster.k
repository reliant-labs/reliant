"""
Positive test — Bundle.config_maps lands on ONE cluster in a multi-cluster env.

The deploy router replicates any object without `forge.dev/cluster` to every
cluster of the env. A ConfigMap defaults to the primary target's NAMESPACE,
which a secondary cluster need not have, so an unstamped one was an apply
failure there. control-plane's hosted-registry ConfigMap rendered exactly that
way in prod: every other object was routed, and it alone would have been sent
to the daemon cluster too.

Pinned here:

  * An unplaced ConfigMap is stamped with the PRIMARY target's cluster — the
    rule forge.Manifests and ingress already follow.
  * A ConfigMap whose labels already name a cluster keeps it: that label is
    how an author places one on another cluster.
  * No ConfigMap is left for the router to replicate.
"""

import forge
import forge.workloads as fw

_primary = forge.ClusterTarget {
    cluster = "gke_example_us-central1_prod"
    namespace = "example-prod"
}

_daemon = forge.Cluster { name = "daemon" }

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
    ]
    config_maps = [
        forge.ConfigMap {name = "app-config", data = {"KEY" = "value"}}
        forge.ConfigMap {
            name = "daemon-config"
            namespace = "daemon-system"
            labels = {"forge.dev/cluster" = _daemon.context}
            data = {"KEY" = "value"}
        }
    ]
    # A second cluster that carries only raw manifests — the shape that makes
    # the env multi-cluster.
    manifests = [
        forge.Manifests {
            cluster = _daemon.context
            objects = [{apiVersion = "v1", kind = "Namespace", metadata = {name = "daemon-system"}}]
        }
    ]
}

_m = forge.render(_bundle).manifests
_key = "forge.dev/cluster"
_cm = lambda name: str -> any {
    [m for m in _m if m.kind == "ConfigMap" and m.metadata.name == name][0]
}

assert_unplaced_config_map_on_primary = _cm("app-config").metadata.labels[_key] == "gke_example_us-central1_prod"
assert_unplaced_config_map_namespace = _cm("app-config").metadata.namespace == "example-prod"
assert_placed_config_map_keeps_its_cluster = _cm("daemon-config").metadata.labels[_key] == "k3d-daemon"

_unattributed = [m.metadata.name for m in _m if m.kind == "ConfigMap" and _key not in m.metadata.labels]
assert_no_unattributed_config_map = len(_unattributed) == 0
