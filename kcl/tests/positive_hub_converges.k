"""
Positive test — Bundle.hub_converges: the delivery units the HUB converges. Every
other unit of a real cluster is applied by forge directly, so an env moves onto
the Flux path one unit at a time. The key is projected only when declared, and
an empty list is a declaration (the hub converges none).
"""

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_prod"
    namespace = "acme-prod"
    connected_cluster = "acme-prod"
    platform = "amd64"
}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    delivery_units = [
        forge.DeliveryUnit {name = "reliant", after = []}
        forge.DeliveryUnit {name = "infra"}
    ]
    hub_converges = ["reliant"]
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", delivery_unit = "reliant", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {name = "db", image = "ghcr.io/acme/db", delivery_unit = "infra", runtime = forge.OnCluster {target = _prod}}
    ]
})

assert_hub_converges_is_projected = output.hub_converges == ["reliant"]
# It states the hub's units, and nothing about forge's own: no applier claim.
assert_no_applier_claim = output.applier == None
