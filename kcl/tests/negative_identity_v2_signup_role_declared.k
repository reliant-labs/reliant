"""
Negative test — identity v2 V3 — signup_role names a declared (converged) role.
"""

# expect: signup_role 'ops' must be a declared role with members_from = "console"
# expect: self-signups would be revoked

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {
        roles = [forge.IdentityRole {key = "ops", members_from = "declared", members = ["a@acme.test"]}]
        signup = "open"
        signup_role = "ops"
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
