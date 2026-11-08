"""
Negative test — identity v2 V14 — a sharer that declares sign-in methods of its own.
"""

# expect: share_with 'staging' reuses that env's instance, so owners, signup, signup_domains, signup_role, methods, passkey_rp_id, social and email belong to the owner

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {
        share_with = "staging"
        methods = ["password", "passkey"]
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
