"""
Positive test — OpenBaoPostgres replaces the Raft default: replicas default to
2 (no quorum to keep), the Cloud SQL proxy needs a Workload Identity GSA, and
the password is a BootstrapSecret.
"""

import forge

_bundle = forge.Bundle {
    project = "fixture"
    env = "prod"
    cluster_target = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "fixture"}
    openbao = forge.OpenBao {
        storage = forge.OpenBaoPostgres {
            cloudsql_instance = "acme:us-central1:pg"
            gcp_service_account = "openbao@acme.iam.gserviceaccount.com"
            password = forge.BootstrapSecret {name = "openbao-pg-password-prod"}
            max_parallel = 4
        }
        consumers = [
            forge.OpenBaoConsumer {
                name = "control-plane-service"
                workload = "admin-server"
                grants = [forge.OpenBaoGrant {path = "secret/data/*", capabilities = ["create", "update"]}]
            }
            forge.OpenBaoConsumer {name = "access-admin", workload = "workspace-controller", delegated = True}
        ]
    }
}

output = forge.render(_bundle)

assert_postgres = _bundle.openbao.storage.type == "postgres"
assert_two_replicas = _bundle.openbao.replicas == 2
assert_consumers = len(_bundle.openbao.consumers) == 2 and _bundle.openbao.consumers[1].delegated
assert_write_grant_needs_no_reason = _bundle.openbao.consumers[0].grants[0].wildcard_read_reason == ""
