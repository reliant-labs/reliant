"""
render contract — Bundle.identities: two managed identities in one cluster env (operators, named "identity", brokered by admin-api; end users, "users", brokered by api), and a gateway that validates both, with the users identity's outputs mapped onto its own config fields. The render carries `identities` (release.Shape.Identities) and no `identity`.
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
    identities = [
        forge.ManagedIdentity {
            roles = [forge.IdentityRole {key = "operator"}]
            applications = [forge.IdentityApplication {name = "console", workload = "admin-api"}]
            consumers = [forge.IdentityConsumer {workload = "admin-api"}, forge.IdentityConsumer {workload = "gateway"}]
        }
        forge.ManagedIdentity {
            name = "users"
            project_name = "acme-app"
            roles = [forge.IdentityRole {key = "member"}]
            applications = [forge.IdentityApplication {name = "app", workload = "api"}]
            consumers = [
                forge.IdentityConsumer {workload = "api"}
                forge.IdentityConsumer {
                    workload = "gateway"
                    fields = forge.IdentityFields {issuer = "users_jwt_issuer", audience = "users_jwt_audience", scopes = "users_oidc_scopes", jwks = "users_jwt_jwks_snapshot"}
                    omit = ["roles_claim"]
                }
            ]
        }
    ]
    workloads = [
        fw.Workload {name = "admin-api", image = "ghcr.io/acme/admin", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {name = "gateway", image = "ghcr.io/acme/gateway", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {
            name = "reporter"
            image = "ghcr.io/acme/reporter"
            env = {USERS_ISSUER = forge.IdentityRef {output = "issuer", identity = "users"}}
            runtime = forge.OnCluster {target = _prod}
        }
    ]
})
