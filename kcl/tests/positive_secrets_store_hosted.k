"""
Positive test — a HostedStore reads Reliant's public secrets endpoint with the
ids the control plane assigned (org, env_id) as the path and the role's tuple.
The role name here is pinned to pkg/secretaccess by
internal/templates/kcl_external_secret_test.go (TestExternalSecretRoleMatchesSecretaccess).
"""

# kcl-args: env=prod

import forge

_daemon = forge.ClusterTarget {cluster = "gke_acme_us-central1-a_prod-daemon", namespace = "env-13b10699", platform = "amd64", connected_cluster = "prod-daemon"}

_out = forge.render(forge.Bundle {
    project = "fixture"
    cluster_target = _daemon
    control_plane = forge.ControlPlane {}
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _daemon}]
    required_secrets = [
        forge.ExternalSecret {
            name = "reliant-db-role"
            namespace = "env-13b10699"
            keys = ["password"]
            source = forge.StoreSource {
                store = forge.HostedStore {org = "55949942-3ee9-4cfc-bb7c-817bdba75186", env_id = "13b10699-40f1-4b19-b273-4b36a9977cc9"}
                data = {"password" = "RELIANT_DB_ROLE_PASSWORD"}
                generate = {"RELIANT_DB_ROLE_PASSWORD" = forge.GeneratedSecret {kind = "password"}}
            }
        }
    ]
})

_ss = [x for x in _out.manifests if x.kind == "SecretStore"][0]
_es = [x for x in _out.manifests if x.kind == "ExternalSecret"][0]
_vault = _ss.spec.provider.vault

assert_public_endpoint = _vault.server == "https://secrets.reliantapi.com"
assert_no_ca_for_a_public_certificate = "caProvider" not in _vault
assert_audience = _vault.auth.jwt.kubernetesServiceAccountToken.audiences == ["https://secrets.reliantapi.com/c/prod-daemon"] and _vault.auth.jwt.path == "jwt-prod-daemon"
assert_path_uses_the_assigned_ids = _es.spec.data[0].remoteRef.key == "tenants/55949942-3ee9-4cfc-bb7c-817bdba75186/13b10699-40f1-4b19-b273-4b36a9977cc9/RELIANT_DB_ROLE_PASSWORD"
assert_store_name = _ss.metadata.name == "reliant-hosted" and _es.spec.secretStoreRef.name == "reliant-hosted"
assert_role_is_derived = _vault.auth.jwt.role == "env-13b10699-prod-daemo-38f1ca55" or _vault.auth.jwt.role.startswith("env-13b10699-")
