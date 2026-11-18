"""
Positive test — the render records what `forge lint --secrets` judges: each
required_secrets entry carries its StoreSource (store kind, data, templates,
generate, pin, immutable, creation policy) or its out_of_band_reason,
and the output carries bootstrap_source. Declarations only; no value.
"""

# kcl-args: env=prod

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

_out = forge.render(forge.Bundle {
    project = "fixture"
    cluster_target = _hub
    bootstrap_source = forge.GCPSecretManager {project = "acme-prod"}
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub, gcp_service_account = "eso@acme-prod.iam.gserviceaccount.com"}]
    required_secrets = [
        forge.ExternalSecret {
            name = "vault"
            namespace = "acme-prod"
            keys = ["key", "dsn"]
            source = forge.StoreSource {
                store = forge.GCPSecretManagerStore {project = "acme-prod"}
                data = {"key" = "vault-key"}
                templates = {"dsn" = "postgres://u:{{ .key | urlquery }}@db/app"}
                pin = {"vault-key" = 1}
                immutable = ["vault-key"]
                creation_policy = "Merge"
                refresh = "15m"
            }
        }
        forge.ExternalSecret {
            name = "cloudflare-api-token"
            namespace = "cert-manager"
            keys = ["api-token"]
            out_of_band_reason = "cert-manager DNS-01 reads it before the store is up"
        }
    ]
})

_by = {s.name: s for s in _out.required_secrets}
_src = _by["vault"].source

assert_bootstrap_source = _out.bootstrap_source.type == "gcpsm" and _out.bootstrap_source.project == "acme-prod"
assert_store = _src.store.type == "gcpsm" and _src.store.project == "acme-prod"
assert_data_and_templates = _src.data.key == "vault-key" and "urlquery" in _src.templates.dsn
assert_pin_and_immutable = _src.pin["vault-key"] == 1 and _src.immutable == ["vault-key"]
assert_policy = _src.creation_policy == "Merge"
assert_unsourced_carries_its_reason = "source" not in _by["cloudflare-api-token"] and _by["cloudflare-api-token"].out_of_band_reason == "cert-manager DNS-01 reads it before the store is up"
assert_sourced_has_no_reason = "out_of_band_reason" not in _by["vault"]
