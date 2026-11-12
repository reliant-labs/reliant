"""
Positive test — a store-backed ExternalSecret on an in-cluster OpenBao renders
the login identity (ServiceAccount, token Role + RoleBinding scoped by
resourceNames), a Vault-provider SecretStore with JWT auth, and an
ExternalSecret carrying the pin as the KV version, the template, the refresh
interval and creationPolicy Orphan + deletionPolicy Retain.
"""

# kcl-args: env=prod

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

_out = forge.render(forge.Bundle {
    project = "fixture"
    cluster_target = _hub
    openbao = forge.OpenBao {}
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub}]
    required_secrets = [
        forge.ExternalSecret {
            name = "app-db-role"
            namespace = "acme-prod"
            keys = ["password", "proxy-database-url"]
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                data = {"password" = "APP_DB_ROLE_PASSWORD"}
                templates = {"proxy-database-url" = "postgres://app:{{ .password | urlquery }}@127.0.0.1:5432/app"}
                pin = {"APP_DB_ROLE_PASSWORD" = 2}
                refresh = "15m"
            }
        }
        forge.ExternalSecret {
            name = "stripe"
            namespace = "acme-prod"
            keys = ["key"]
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                data = {"key" = "STRIPE_SECRET_KEY"}
            }
        }
    ]
})

_m = _out.manifests
_of = lambda kind: str -> [any] {
    [x for x in _m if x.kind == kind]
}
_sa = _of("ServiceAccount")
_role = _of("Role")
_rb = _of("RoleBinding")
_ss = _of("SecretStore")
_es = _of("ExternalSecret")
_by = lambda name: str -> any {
    [x for x in _es if x.metadata.name == name][0]
}
_db = _by("app-db-role")
_stripe = _by("stripe")
_vault = _ss[0].spec.provider.vault

assert_one_login_identity_per_namespace = len(_sa) == 1 and _sa[0].metadata.name == "reliant-secrets" and _sa[0].metadata.namespace == "acme-prod" and _sa[0].automountServiceAccountToken == False
assert_token_role_names_the_service_account = _role[0].rules == [{apiGroups = [""], resources = ["serviceaccounts/token"], resourceNames = ["reliant-secrets"], verbs = ["create"]}]
assert_token_role_bound_to_eso = _rb[0].subjects == [{kind = "ServiceAccount", name = "external-secrets", namespace = "external-secrets"}] and _rb[0].roleRef.name == _role[0].metadata.name
assert_one_store_per_kind_per_namespace = len(_ss) == 1 and _ss[0].kind == "SecretStore" and _ss[0].apiVersion == "external-secrets.io/v1"
assert_vault_jwt_login = _vault.auth.jwt.path == "jwt-hub" and _vault.auth.jwt.kubernetesServiceAccountToken.serviceAccountRef.name == "reliant-secrets"
assert_audience_pins_the_cluster = _vault.auth.jwt.kubernetesServiceAccountToken.audiences == ["https://openbao.openbao.svc:8200/c/hub"]
assert_in_cluster_service_and_ca = _vault.server == "https://openbao.openbao.svc:8200" and _vault.caProvider == {type = "ConfigMap", name = "openbao-ca", key = "ca.crt"}
assert_kv_v2_on_the_component_mount = _vault.version == "v2" and _vault.path == "secret"
assert_pin_is_the_kv_version = _db.spec.data[0].remoteRef == {key = "tenants/fixture/prod/APP_DB_ROLE_PASSWORD", property = "value", version = "2"}
assert_unpinned_floats = "version" not in _stripe.spec.data[0].remoteRef
assert_template_v2_merge = _db.spec.target.template == {engineVersion = "v2", mergePolicy = "Merge", data = {"proxy-database-url" = "postgres://app:{{ .password | urlquery }}@127.0.0.1:5432/app"}}
assert_no_template_without_templates = "template" not in _stripe.spec.target
assert_outage_safe_policies = _db.spec.target.creationPolicy == "Orphan" and _db.spec.target.deletionPolicy == "Retain" and _db.spec.target.name == "app-db-role"
assert_refresh = _db.spec.refreshInterval == "15m" and _stripe.spec.refreshInterval == "1h"
assert_store_ref = _db.spec.secretStoreRef == {name = "reliant-openbao", kind = "SecretStore"}
assert_no_cluster_kinds = len([x for x in _m if x.kind in ["ClusterSecretStore", "ClusterExternalSecret", "PushSecret"]]) == 0
assert_role_is_derived = _vault.auth.jwt.role == "env-prod-hub-0f864f38"
