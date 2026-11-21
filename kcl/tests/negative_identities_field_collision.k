"""
Negative test — one workload consumes two identities with no remapped fields: both would fill jwt_issuer.
"""

# expect: workload 'gateway' consumes identities
# expect: JWT_ISSUER (from identities
# expect: forge.IdentityFields {issuer = "users_jwt_issuer"

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identities = [
        forge.ManagedIdentity {
            applications = [forge.IdentityApplication {name = "console", workload = "admin-api"}]
            consumers = [forge.IdentityConsumer {workload = "admin-api"}, forge.IdentityConsumer {workload = "gateway"}]
        }
        forge.ManagedIdentity {
            name = "users"
            applications = [forge.IdentityApplication {name = "app", workload = "api"}]
            consumers = [forge.IdentityConsumer {workload = "api"}, forge.IdentityConsumer {workload = "gateway"}]
        }
    ]
    workloads = [
        fw.Workload {name = "admin-api", image = "ghcr.io/acme/admin", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {name = "gateway", image = "ghcr.io/acme/gateway", runtime = forge.OnCluster {target = _prod}}
    ]
})
