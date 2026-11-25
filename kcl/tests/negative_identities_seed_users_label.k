"""
Negative test — a per-identity rule names the identity it judged.
"""

# expect: Bundle.identities[users].seed_users create users with known passwords

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
            consumers = [forge.IdentityConsumer {workload = "admin-api"}]
        }
        forge.ManagedIdentity {
            name = "users"
            applications = [forge.IdentityApplication {name = "app", workload = "api"}]
            consumers = [forge.IdentityConsumer {workload = "api"}]
            seed_users = [forge.IdentitySeedUser {email = "dev@acme.test"}]
        }
    ]
    workloads = [
        fw.Workload {name = "admin-api", image = "ghcr.io/acme/admin", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
    ]
})
