"""
Negative test — sharing a host-run IdP.
"""

# expect: needs a hosted instance to share

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    infra = [forge.HostInfra {name = "idp", engine = "zitadel", port = 8098, idp_database_port = 5432}]
    identity = forge.ManagedIdentity {share_with = "staging", runtime = forge.IdentityOnHost {infra = "idp"}}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
