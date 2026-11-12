"""
Positive test — a Secret made of `templates` alone (a dev constant, a DSN) reads
no store value. ESO returns early on a sync whose provider returned no data and
deletionPolicy Retain ("secret retained due to DeletionPolicy=Retain"), so under
Retain the Secret is never created and every pod mounting it sits in
CreateContainerConfigError. Merge builds it from the template and removes nothing
it did not write. A source that reads a store value keeps Retain.
"""

# kcl-args: env=prod

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64"}

_out = forge.render(forge.Bundle {
    project = "fixture"
    cluster_target = _hub
    openbao = forge.OpenBao {}
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub}]
    required_secrets = [
        forge.ExternalSecret {
            name = "constants"
            namespace = "acme-prod"
            keys = ["url"]
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                templates = {"url" = "postgres://db"}
            }
        }
        forge.ExternalSecret {
            name = "mixed"
            namespace = "acme-prod"
            keys = ["token", "url"]
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                data = {"token" = "SERVICE_TOKEN"}
                templates = {"url" = "postgres://db"}
            }
        }
    ]
})

_es = {x.metadata.name: x for x in _out.manifests if x.kind == "ExternalSecret"}

assert_template_only_merges = _es["constants"].spec.target.deletionPolicy == "Merge" and _es["constants"].spec.target.creationPolicy == "Orphan"
assert_store_backed_retains = _es["mixed"].spec.target.deletionPolicy == "Retain"
