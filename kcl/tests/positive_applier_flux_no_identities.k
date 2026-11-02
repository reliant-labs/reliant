"""
Positive test — `applier = "flux"` with `apply_identities = False`, a named
namespace and a pull Secret to copy.

No identities is the opt-out: kustomize-controller keeps the distribution's
own RBAC (it applies as cluster-admin), so no apply identity is rendered and
no namespace binding is needed. The ResourceSet still runs as its own
two-kind identity.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "staging"
    cluster_target = _k3d
    applier = "flux"
    flux_delivery = forge.FluxDelivery {
        apply_identities = False
        namespace = "acme-delivery"
        pull_secret_from = "acme-dev/registry-pull"
    }
    workloads = [fw.Workload {name = "api", image = "localhost:5050/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})

_fd = output.flux_delivery
_chart = [h for h in output.helm_charts if h.name == "flux-delivery"][0]
_objs = _chart.manifests
_instance = [o for o in _objs if o.kind == "FluxInstance"][0]

assert_projected = _fd.apply_identities == False and _fd.namespace == "acme-delivery"
assert_pull_secret = _fd.pull_secret_from == "acme-dev/registry-pull" and _fd.pull_secret == forge.FLUX_DELIVERY_PULL_SECRET
assert_no_flux_lockdown = _instance.spec.cluster.multitenant == False
assert_no_apply_identities = [o.metadata.name for o in _objs if o.kind == "ServiceAccount" and o.metadata.namespace == "acme-delivery"] == [forge.FLUX_DELIVERY_SA]
assert_no_namespace_bindings = [o.metadata.name for o in _objs if o.kind == "RoleBinding" and o.metadata.namespace == "acme-delivery"] == [forge.FLUX_DELIVERY_SA]
assert_resourceset_in_named_namespace = [o.metadata.namespace for o in _objs if o.kind == "ResourceSet"] == ["acme-delivery"]
