"""
Positive test — `ClusterTarget.connected_cluster` names the cluster that
`forge cluster connect` registered, so a control-plane-backed env can deploy
into a cluster the author operates.

TWO FIELDS FOR THE SAME PHYSICAL CLUSTER, through two different authorities.
`cluster` is a kubectl context: a local fact about the machine running forge,
which differs between laptops and does not exist in CI. `connected_cluster` is
an org-level resource in the control plane, holding the address, CA and
credential the platform deploys with. An env that declares a control plane and
targets a real cluster needs both — the context to render and apply locally,
the connected name so the platform knows where to ship.

It is OPTIONAL, and unset is correct for a k3d cluster: forge applies to one
directly and the control plane never dials it. The Go-side render refusal
(refuseUnboundClusterTargets) is what rejects an unset value on a NON-k3d
cluster in an env that declares a control plane; it lives there rather than in
a KCL check because it needs to know whether the env declares a control plane
and whether the context is one forge manages.
"""

import forge
import forge.workloads as fw

# A connected cloud cluster: both names stated.
_prod = forge.ClusterTarget {
    cluster = "gke_acme_us-central1_prod"
    namespace = "acme-prod"
    connected_cluster = "prod-us"
    platform = "amd64"
}

# A local cluster: no connected cluster, because there is nothing to connect.
_dev = forge.ClusterTarget {
    cluster = "k3d-demo"
    namespace = "demo-dev"
}

_bundle = forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {
        endpoint = "https://cp.example.test"
    }
    cluster_target = _prod
    workloads = [
        fw.Workload {
            name = "api"
            image = "ghcr.io/acme/api"
            runtime = forge.OnCluster {target = _prod}
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
        }
    ]
}

output = forge.render(_bundle)

# The field survives the render on BOTH the primary target and the workload's
# own runtime. Both matter: forge derives one binding per rendered cluster from
# whichever of them names the context, so a field that lowered on only one of
# the two would silently drop an env's second cluster.
assert_primary_connected = output.cluster_target.connected_cluster == "prod-us"
assert_primary_context = output.cluster_target.cluster == "gke_acme_us-central1_prod"

_api = [w for w in output.workloads if w.name == "api"][0]
assert_runtime_is_cluster = _api.runtime.type == "cluster"
assert_runtime_connected = _api.runtime.connected_cluster == "prod-us"
assert_runtime_context = _api.runtime.cluster == "gke_acme_us-central1_prod"

# Unset lowers to null rather than to the empty string, which is what keeps
# "declares no connected cluster" distinguishable on the Go side from
# "declares one that happens to be blank".
_local = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = _dev
    workloads = [
        fw.Workload {
            name = "api"
            image = "ghcr.io/acme/api"
            runtime = forge.OnCluster {target = _dev}
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
        }
    ]
})
assert_k3d_unset = _local.cluster_target.connected_cluster == None
