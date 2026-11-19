"""
Negative test — identity v2 V11 — a passkey RP id that is not a DNS name.
"""

# expect: passkey_rp_id 'https://acme.test' must be a lower-case DNS name

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {
        methods = ["passkey"]
        passkey_rp_id = "https://acme.test"
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
