"""
Positive test — forge.ManagedIdentity on a cluster env: the declaration is
roles only; applications, consumers, the delivered Secrets and each consumer's
typed config are all derived.
"""

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {
    cluster = "gke_acme_prod"
    namespace = "acme-prod"
    connected_cluster = "acme-prod"
    platform = "amd64"
}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identity = forge.ManagedIdentity {
        roles = [
            forge.IdentityRole {key = "admin", display_name = "Administrators"}
            forge.IdentityRole {key = "member"}
        ]
    }
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {name = "worker", kind = "worker", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
    ]
})

_by = {w.name: w for w in output.workloads}
_env = {e.name: e for e in _by["api"].spec.env}
_id = output.identity

assert_one_application = len(_id.applications) == 1
assert_application_is_derived = _id.applications[0].name == "prod:api" and _id.applications[0].kind == "native_broker"
assert_runtime_defaults_to_hosted = _id.runtime == "hosted"
assert_login_defaults_to_native = _id.login.mode == "native"
assert_consumer_is_the_service = [c.workload for c in _id.consumers] == ["api"]
assert_issuer_from_the_delivered_secret = _env["JWT_ISSUER"].secretRef.name == "forge-identity-identity" and _env["JWT_ISSUER"].secretRef.key == "ISSUER"
assert_audience = _env["JWT_AUDIENCE"].secretRef.key == "AUDIENCE"
assert_client_id = _env["OIDC_CLIENT_ID"].secretRef.key == "PROD_API_CLIENT_ID"
assert_redirect = _env["OIDC_REDIRECT_URI"].secretRef.key == "PROD_API_REDIRECT_URI"
assert_idp_base = _env["IDP_BASE"].secretRef.key == "ISSUER"
assert_roles_claim_is_a_literal = _env["JWT_ROLES_CLAIM"].value == "auto"
assert_broker_token_is_a_file = _env["IDP_BROKER_TOKEN_FILE"].value == "/var/run/secrets/forge/identity/broker_token" and "IDP_BROKER_TOKEN" not in _env
assert_jwks_is_a_file = _env["JWT_JWKS_SNAPSHOT_FILE"].value == "/var/run/secrets/forge/identity-jwks/jwks.json"
assert_worker_gets_nothing = "JWT_ISSUER" not in {e.name: e for e in _by["worker"].spec.env}

_secrets = {s.name: s for s in output.control_plane_secrets}
assert_three_deliveries = sorted(_secrets) == ["forge-identity-identity", "forge-identity-identity-jwks"]
assert_identity_secret_is_required = _secrets["forge-identity-identity"].required == True and _secrets["forge-identity-identity"].kind == "identity"
assert_identity_keys = _secrets["forge-identity-identity"].keys == ["AUDIENCE", "ISSUER", "PROD_API_BROKER_TOKEN", "PROD_API_CLIENT_ID", "PROD_API_LOGIN_URI", "PROD_API_REDIRECT_URI", "SCOPES"]
assert_jwks_secret_is_optional = _secrets["forge-identity-identity-jwks"].kind == "identity_jwks" and not _secrets["forge-identity-identity-jwks"].required
assert_deliveries_in_identity = sorted([d.name for d in _id.deliveries]) == ["forge-identity-identity", "forge-identity-identity-jwks"]
assert_connected = _secrets["forge-identity-identity"].connected_cluster == "acme-prod"

_vols = {v.name: v for v in _by["api"].spec.volumes}
assert_token_volume = _vols["forge-identity"].source.secret.name == "forge-identity-identity" and "optional" not in _vols["forge-identity"].source.secret
assert_jwks_volume_optional = _vols["forge-identity-jwks"].source.secret.optional == True

# A config-projected IDP_BROKER_TOKEN secret ref does not survive beside the
# file that replaces it.
_cfg = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identity = forge.ManagedIdentity {}
    workloads = [fw.Workload {
        name = "api", image = "ghcr.io/acme/api"
        env = {
            IDP_BROKER_TOKEN = forge.SecretRef {name = "acme-secrets", key = "idp_broker_token", store_key = "IDP_BROKER_TOKEN"}
            JWT_ISSUER = ""
            OIDC_SCOPES = "openid,profile,email,offline_access"
        }
        runtime = forge.OnCluster {target = _prod}
    }]
})
_cfg_env = {e.name: e for e in _cfg.workloads[0].spec.env}
assert_projected_token_is_replaced = "IDP_BROKER_TOKEN" not in _cfg_env and "IDP_BROKER_TOKEN_FILE" in _cfg_env
assert_empty_projection_does_not_conflict = _cfg_env["JWT_ISSUER"].secretRef.key == "ISSUER"
assert_identity_scopes_replace_the_default = _cfg_env["OIDC_SCOPES"].secretRef.key == "SCOPES"
