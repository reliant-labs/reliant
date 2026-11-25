"""
Positive test — a consumer declines outputs its config has no field for. The
omitted outputs are not projected (no env, no volume), the Secrets still carry
every key, and the shape records the omission.
"""

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identity = forge.ManagedIdentity {
        applications = [forge.IdentityApplication {name = "console", workload = "admin-api", kind = "native_broker"}]
        consumers = [forge.IdentityConsumer {
            workload = "admin-api"
            fields = forge.IdentityFields {issuer = "zitadel_issuer", broker_token = "zitadel_broker_token"}
            omit = ["scopes", "roles_claim", "login_uri", "jwks"]
        }]
    }
    workloads = [
        fw.Workload {name = "admin-api", image = "ghcr.io/acme/admin", runtime = forge.OnCluster {target = _prod}}
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}
    ]
})

_by = {w.name: w for w in output.workloads}
_env = {e.name: e for e in _by["admin-api"].spec.env}
_vols = [v.name for v in _by["admin-api"].spec.volumes]

assert_omitted_outputs_are_not_projected = all n in ["OIDC_SCOPES", "JWT_ROLES_CLAIM", "IDP_LOGIN_URI", "JWT_JWKS_SNAPSHOT_FILE"] { n not in _env }
assert_the_rest_is_projected = _env["ZITADEL_ISSUER"].secretRef.key == "ISSUER" and _env["JWT_AUDIENCE"].secretRef.key == "AUDIENCE" and _env["OIDC_CLIENT_ID"].secretRef.key == "CONSOLE_CLIENT_ID" and _env["IDP_BASE"].secretRef.key == "ISSUER"
assert_broker_token_still_a_file = _env["ZITADEL_BROKER_TOKEN_FILE"].value == "/var/run/secrets/forge/identity/broker_token"
assert_no_jwks_volume_without_the_output = "forge-identity-jwks" not in _vols and "forge-identity" in _vols
_secrets = {s.name: s for s in output.control_plane_secrets}
assert_secrets_still_carry_every_key = "SCOPES" in _secrets["forge-identity-identity"].keys and "CONSOLE_LOGIN_URI" in _secrets["forge-identity-identity"].keys
assert_shape_records_omit_sorted = output.identity.consumers[0].omit == ["jwks", "login_uri", "roles_claim", "scopes"]
