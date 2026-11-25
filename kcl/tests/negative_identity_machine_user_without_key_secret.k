"""
Negative test — a hosted machine user with nowhere to deliver its key.
"""

# expect: have no key_secret/cluster

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {
        roles = [forge.IdentityRole {key = "admin"}]
        machine_users = [forge.IdentityMachineUser {name = "ops", roles = ["admin"]}]
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
