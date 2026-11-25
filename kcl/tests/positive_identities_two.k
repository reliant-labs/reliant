"""
Positive test — Bundle.identities: one env, two managed identities. The
operators' identity (named "identity", the default) is brokered by admin-api;
the app's end users ("users") by api. A gateway validates both issuers: it
maps the users identity's outputs onto its own config fields, so each field
has one source. Each identity gets its own delivered Secrets, and each
application lives in exactly one of them.
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

_by = {w.name: w for w in output.workloads}
_env = lambda name: str -> {str:} {
    {e.name: e for e in _by[name].spec.env}
}
_vols = lambda name: str -> {str:} {
    {v.name: v for v in (_by[name].spec.volumes or [])}
}

# The shape: several identities are `identities`, never `identity`.
assert_identities_in_declaration_order = [i.name for i in output.identities] == ["identity", "users"]
assert_no_single_identity_key = "identity" not in output
assert_each_has_its_own_application = [[a.name for a in i.applications] for i in output.identities] == [["console"], ["app"]]
assert_users_project = output.identities[1].project_name == "acme-app"
assert_gateway_fields_recorded = output.identities[1].consumers[1].fields == {issuer = "users_jwt_issuer", audience = "users_jwt_audience", scopes = "users_oidc_scopes", jwks = "users_jwt_jwks_snapshot"}

# One Secret pair per identity, never shared.
_secrets = sorted([s.name for s in output.control_plane_secrets])
assert_one_secret_pair_per_identity = _secrets == ["forge-identity-identity", "forge-identity-identity-jwks", "forge-identity-users", "forge-identity-users-jwks"]
assert_deliveries_are_per_identity = sorted([d.name for d in output.identities[1].deliveries]) == ["forge-identity-users", "forge-identity-users-jwks"]

# Each broker reads its own identity.
assert_admin_reads_operators = _env("admin-api")["JWT_ISSUER"].secretRef.name == "forge-identity-identity" and _env("admin-api")["OIDC_CLIENT_ID"].secretRef.key == "CONSOLE_CLIENT_ID"
assert_api_reads_users = _env("api")["JWT_ISSUER"].secretRef.name == "forge-identity-users" and _env("api")["OIDC_CLIENT_ID"].secretRef.key == "APP_CLIENT_ID"
assert_api_broker_token = _env("api")["IDP_BROKER_TOKEN_FILE"].value == "/var/run/secrets/forge/identity/broker_token" and _vols("api")["forge-identity"].source.secret.name == "forge-identity-users"

# The gateway reads both, each under its own fields, files and volumes.
_gw = _env("gateway")
assert_gateway_operator_issuer = _gw["JWT_ISSUER"].secretRef.name == "forge-identity-identity"
assert_gateway_users_issuer = _gw["USERS_JWT_ISSUER"].secretRef.name == "forge-identity-users" and _gw["USERS_JWT_ISSUER"].secretRef.key == "ISSUER"
assert_gateway_users_scopes = _gw["USERS_OIDC_SCOPES"].secretRef.name == "forge-identity-users"
assert_gateway_one_roles_claim = _gw["JWT_ROLES_CLAIM"].value == "auto" and "USERS_JWT_ROLES_CLAIM" not in _gw
assert_gateway_operator_jwks_file = _gw["JWT_JWKS_SNAPSHOT_FILE"].value == "/var/run/secrets/forge/identity-jwks/jwks.json"
assert_gateway_users_jwks_file = _gw["USERS_JWT_JWKS_SNAPSHOT_FILE"].value == "/var/run/secrets/forge/identity-jwks--users/jwks.json"
assert_gateway_volumes = sorted(_vols("gateway")) == ["forge-identity-jwks", "forge-identity-jwks--users"]
assert_gateway_users_volume = _vols("gateway")["forge-identity-jwks--users"].mountPath == "/var/run/secrets/forge/identity-jwks--users" and _vols("gateway")["forge-identity-jwks--users"].source.secret.name == "forge-identity-users-jwks"
assert_gateway_brokers_nothing = "OIDC_CLIENT_ID" not in _gw and "IDP_BROKER_TOKEN_FILE" not in _gw

# An explicit IdentityRef names the identity it reads.
assert_named_ref = _env("reporter")["USERS_ISSUER"].secretRef.name == "forge-identity-users" and _env("reporter")["USERS_ISSUER"].secretRef.key == "ISSUER"
