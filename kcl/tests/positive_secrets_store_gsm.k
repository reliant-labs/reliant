"""
Positive test — a GCPSecretManagerStore renders a gcpsm SecretStore that logs
in with ESO's own Workload Identity (no stored credential, no per-namespace
service account or token Role), with names as secret ids and the pin as the
GSM version.
"""

# kcl-args: env=prod

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64"}

_out = forge.render(forge.Bundle {
    project = "fixture"
    cluster_target = _hub
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub, gcp_service_account = "eso@acme-prod.iam.gserviceaccount.com"}]
    required_secrets = [
        forge.ExternalSecret {
            name = "cloudflare-api-token"
            namespace = "cert-manager"
            keys = ["api-token"]
            source = forge.StoreSource {
                store = forge.GCPSecretManagerStore {project = "acme-prod"}
                data = {"api-token" = "cloudflare-api-token"}
                pin = {"cloudflare-api-token" = 3}
            }
        }
    ]
})

_m = _out.manifests
_ss = [x for x in _m if x.kind == "SecretStore"]
_es = [x for x in _m if x.kind == "ExternalSecret"][0]

assert_gcpsm_provider = _ss[0].spec.provider == {gcpsm = {projectID = "acme-prod"}}
assert_ambient_workload_identity = "auth" not in _ss[0].spec.provider.gcpsm
assert_secret_id_and_version = _es.spec.data[0].remoteRef == {key = "cloudflare-api-token", version = "3"}
assert_no_login_identity = len([x for x in _m if x.kind in ["ServiceAccount", "Role", "RoleBinding"]]) == 0
assert_lands_in_its_own_namespace = _es.metadata.namespace == "cert-manager" and _ss[0].metadata.namespace == "cert-manager"
assert_store_name = _ss[0].metadata.name == "reliant-gsm-acme-prod"
