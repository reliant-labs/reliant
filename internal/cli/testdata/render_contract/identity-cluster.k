"""
render contract — a CLUSTER env with a managed identity: the declaration is roles only. Applications, consumers, the delivered Secrets (identity, identity_jwks) and the API's typed config (issuer, audience, client id, redirect, idp base, roles claim, broker-token and JWKS-snapshot files) are all derived.
"""
# kcl-args: env="prod"

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identity = forge.ManagedIdentity {
        roles = [forge.IdentityRole {key = "admin"}, forge.IdentityRole {key = "member"}]
    }
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnCluster {target = _prod}}
        fw.Workload {name = "worker", kind = "worker", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
    ]
})
