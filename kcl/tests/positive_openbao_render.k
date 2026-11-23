"""
Positive test — forge.OpenBao lowers to ONE OpenBao record Go expands, a resolved
`openbao` block clients read, a Namespace for the store, and (while the bootstrap
source is out of band) the Secrets it needs before it can start. A store-backed
ExternalSecret in another namespace becomes an exact-name binding, and that
namespace is admitted and gets the CA.
"""

# kcl-args: env=prod

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

_bundle = forge.Bundle {
    project = "acme"
    cluster_target = _hub
    openbao = forge.OpenBao {
        storage = forge.OpenBaoPostgres {
            cloudsql_instance = "acme:us-central1:pg"
            gcp_service_account = "openbao@acme.iam.gserviceaccount.com"
            password = forge.BootstrapSecret {name = "openbao-pg-password-prod"}
        }
        listeners = [
            forge.OpenBaoListener {port = 8200}
            forge.OpenBaoListener {port = 8300, tls = False, transitional_reason = "clients move to https by 2026-12-01"}
        ]
        consumers = [
            forge.OpenBaoConsumer {
                name = "control-plane-service"
                workload = "admin-server"
                namespace = "acme-prod"
                grants = [forge.OpenBaoGrant {path = "secret/data/*", capabilities = ["create", "update"]}]
            }
            forge.OpenBaoConsumer {
                name = "deployer"
                workload = "deployer"
                namespace = "acme-prod"
                grants = [forge.OpenBaoGrant {
                    path = "secret/data/tenants/*"
                    capabilities = ["read"]
                    wildcard_read_reason = "until per-env grants land"
                }]
            }
        ]
    }
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub}]
    required_secrets = [
        forge.ExternalSecret {
            name = "stripe"
            namespace = "payments"
            keys = ["key"]
            reason = "stripe"
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                data = {"key" = "STRIPE_KEY"}
            }
        }
        forge.ExternalSecret {
            name = "other"
            namespace = "payments"
            keys = ["token", "dsn"]
            reason = "x"
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                data = {"token" = "TOKEN"}
                templates = {"dsn" = "u:{{ .token }}"}
            }
        }
    ]
}

output = forge.render(_bundle)

_ob = [m for m in output.manifests if m.kind == "OpenBao"]
_ns = [m.metadata.name for m in output.manifests if m.kind == "Namespace"]
assert_one_record = len(_ob) == 1 and _ob[0].spec.owner == "acme.prod"
assert_postgres = _ob[0].spec.storage.type == "postgres" and _ob[0].spec.storage.postgres.cloudSQLInstance == "acme:us-central1:pg"
assert_replicas = _ob[0].spec.replicas == 2
assert_namespace_rendered = "openbao" in _ns and "acme-prod" in _ns
assert_names_grouped = _ob[0].spec.access.bindings == [{"cluster": "hub", "namespace": "payments", "names": ["STRIPE_KEY", "TOKEN"]}]
assert_ingress = _ob[0].spec.ingressNamespaces == ["external-secrets", "payments"] and _ob[0].spec.caNamespaces == ["payments"]
assert_block = output.openbao.server_url == "https://openbao.openbao.svc:8200" and output.openbao.auth_mount == "jwt-hub" and output.openbao.self_cluster == "hub"
assert_audience = output.openbao.audience == "https://openbao.openbao.svc:8200/c/hub"
assert_warnings = len(output.openbao.warnings) == 3 and "PLAINTEXT" in output.openbao.warnings[0] and "until per-env grants land" in output.openbao.warnings[1] and "inside the cluster they protect" in output.openbao.warnings[2]
assert_prereqs_for_oob = sorted([s.name for s in output.required_secrets if s.namespace == "openbao"]) == ["openbao-storage", "openbao-unseal"]
