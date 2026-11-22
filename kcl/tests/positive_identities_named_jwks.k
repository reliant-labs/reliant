"""
Positive test — an identity named "jwks" whose broker also validates the
default identity. Each identity's file outputs get their own volume and mount
path: the named one is suffixed "--jwks", so its broker-token volume is
forge-identity--jwks, never forge-identity-jwks (the default identity's
signing-key snapshot).
"""

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
            consumers = [forge.IdentityConsumer {workload = "admin-api"}, forge.IdentityConsumer {workload = "api"}]
        }
        forge.ManagedIdentity {
            name = "jwks"
            project_name = "acme-app"
            roles = [forge.IdentityRole {key = "member"}]
            applications = [forge.IdentityApplication {name = "app", workload = "api"}]
            consumers = [forge.IdentityConsumer {
                workload = "api"
                fields = forge.IdentityFields {issuer = "app_jwt_issuer", audience = "app_jwt_audience", scopes = "app_oidc_scopes", jwks = "app_jwt_jwks_snapshot"}
                omit = ["roles_claim"]
            }]
        }
    ]
    workloads = [
        fw.Workload {name = "admin-api", image = "ghcr.io/acme/admin", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
    ]
})

_api = [w for w in output.workloads if w.name == "api"][0]
_vols = [v for v in (_api.spec.volumes or [])]
_names = [v.name for v in _vols]
_paths = [v.mountPath for v in _vols]

assert_volume_names_distinct = len(_names) == len({n: 0 for n in _names})
assert_mount_paths_distinct = len(_paths) == len({p: 0 for p in _paths})
assert_default_snapshot_kept = "forge-identity-jwks" in _names and [v.source.secret.name for v in _vols if v.name == "forge-identity-jwks"] == ["forge-identity-identity-jwks"]
assert_named_broker_volume = [v.mountPath for v in _vols if v.name == "forge-identity--jwks"] == ["/var/run/secrets/forge/identity--jwks"]
