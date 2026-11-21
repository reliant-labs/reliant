"""
Positive test — a workload may declare BOTH RBAC tiers, and an operator may
declare namespacedRBAC.

The tiers ADD; neither replaces the other. pkg/deploy.RenderWorkloads gives
the workload's one ServiceAccount a namespaced Role (the config-read
defaults + namespacedRBAC) and, when it has cluster-scoped intent, a
ClusterRole carrying ONLY that intent (clusterRBAC, and an operator's CRD
rules). The config-read defaults are never cluster-wide: a ClusterRole that
carried them was read access to every Secret in the cluster.

So a sweeper that writes ConfigMaps in its own namespace AND lists a CRD
across namespaces states each grant at its own scope, instead of widening
the namespaced one into its ClusterRole to satisfy an exclusivity rule.
The render must succeed and the Workload record must carry both lists
verbatim (the Go renderer is what turns them into objects; see
TestBothRBACTiersCoexist).
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {cluster = "k3d-dev", namespace = "dev"}
_ns_rule = {apiGroups = [""], resources = ["configmaps"], verbs = ["get", "create"]}
_cluster_rule = {apiGroups = ["reliant.dev"], resources = ["workspaces"], verbs = ["list", "watch"]}

output = forge.render(forge.Bundle {
    project = "dev"
    workloads = [_w | {runtime = forge.OnCluster {target = _target}} if not _w.runtime else _w for _w in [
        fw.Workload {
            name = "sweeper"
            kind = "worker"
            image = "ghcr.io/acme/sweeper:v1"
            args = ["sweep"]
            namespacedRBAC = [_ns_rule]
            clusterRBAC = [_cluster_rule]
        }
        fw.Workload {
            name = "ctrl"
            kind = "operator"
            image = "ghcr.io/acme/ctrl:v1"
            crds = ["Widget"]
            group = "acme.dev"
            namespacedRBAC = [_ns_rule]
        }
    ]]
})

_spec = {w.name: w.spec for w in output.workloads}

assert_worker_keeps_namespaced = _spec["sweeper"].namespacedRBAC == [_ns_rule]
assert_worker_keeps_cluster = _spec["sweeper"].clusterRBAC == [_cluster_rule]
assert_operator_keeps_namespaced = _spec["ctrl"].namespacedRBAC == [_ns_rule]
