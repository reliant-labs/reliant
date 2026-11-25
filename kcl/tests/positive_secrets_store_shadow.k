"""
Positive test — migration (§7.1): `shadow` syncs to `<name>-eso-shadow` so the
live Secret can be compared before adoption; a Merge source updates only its
declared keys of an existing Secret, and a shadow never touches the live one.
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
            name = "control-plane-secrets"
            namespace = "acme-prod"
            keys = ["stripe"]
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                data = {"stripe" = "STRIPE_SECRET_KEY"}
                shadow = True
                creation_policy = "Merge"
            }
        }
        forge.ExternalSecret {
            name = "adopted"
            namespace = "acme-prod"
            keys = ["token"]
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                data = {"token" = "SERVICE_TOKEN"}
                creation_policy = "Merge"
            }
        }
    ]
})

_es = [x for x in _out.manifests if x.kind == "ExternalSecret"]
_shadow = [x for x in _es if x.metadata.name == "control-plane-secrets-eso-shadow"]
_adopted = [x for x in _es if x.metadata.name == "adopted"][0]

assert_shadow_target = len(_shadow) == 1 and _shadow[0].spec.target.name == "control-plane-secrets-eso-shadow"
assert_no_ExternalSecret_writes_the_live_secret = len([x for x in _es if x.spec.target.name == "control-plane-secrets"]) == 0
assert_shadow_never_merges = _shadow[0].spec.target.creationPolicy == "Orphan" and _shadow[0].spec.target.deletionPolicy == "Retain"
assert_adopted_merges = _adopted.spec.target.creationPolicy == "Merge" and _adopted.spec.target.deletionPolicy == "Retain"
