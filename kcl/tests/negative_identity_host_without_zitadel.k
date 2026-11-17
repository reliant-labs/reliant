"""
Negative test — IdentityOnHost naming infra that is not a zitadel.
"""

# expect: is not a forge.HostInfra with engine 'zitadel'

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    infra = [forge.HostInfra {name = "pg", engine = "postgres", port = 5432}]
    identity = forge.ManagedIdentity {runtime = forge.IdentityOnHost {infra = "pg"}}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
