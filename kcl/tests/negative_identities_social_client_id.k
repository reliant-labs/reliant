"""
Negative test — two identities in one env share a social OAuth client.
"""

# expect: social client_id '1234.apps.googleusercontent.com' is declared by identities
# expect: each issuer has its own callback, so each needs its own OAuth app

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
            social = [forge.IdentityGoogle {client_id = "1234.apps.googleusercontent.com", client_secret = forge.ManagedSecret {name = "OPS_GOOGLE_SECRET"}}]
        }
        forge.ManagedIdentity {
            name = "users"
            applications = [forge.IdentityApplication {name = "app", workload = "api"}]
            consumers = [forge.IdentityConsumer {workload = "api"}]
            social = [forge.IdentityGoogle {client_id = "1234.apps.googleusercontent.com", client_secret = forge.ManagedSecret {name = "USERS_GOOGLE_SECRET"}}]
        }
    ]
    workloads = [
        fw.Workload {name = "admin-api", image = "ghcr.io/acme/admin", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
    ]
})
