"""
Negative test — identity v2 V8 — owners on a role whose members are a converged set.
"""

# expect: owner_role 'admin' must be a declared role
# expect: owners are never demoted

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {
        roles = [forge.IdentityRole {key = "admin", members_from = "declared", members = ["a@acme.test"]}]
        owners = ["boss@acme.test"]
        owner_role = "admin"
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
