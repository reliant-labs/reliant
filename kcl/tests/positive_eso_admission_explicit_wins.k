"""
Positive test — an explicit ESOAdmission list replaces the derived default for
that list only; the lists left empty are still derived.
"""

# kcl-args: env=prod

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "fixture"
    cluster_target = _hub
    openbao = forge.OpenBao {}
    external_secrets_operators = [forge.ExternalSecretsOperator {
        cluster = _hub
        admission = forge.ESOAdmission {servers = ["https://elsewhere.example:8200"]}
    }]
    required_secrets = [forge.ExternalSecret {
        name = "a"
        namespace = "acme-prod"
        keys = ["k"]
        source = forge.StoreSource {store = forge.OpenBaoStore {}, data = {"k" = "A"}}
    }]
})

_pin = [o for o in output.helm_charts[0].values.extraObjects if o.kind == "ValidatingAdmissionPolicy" and o.metadata.name == "eso-store-pin"][0]
_expr = _pin.spec.validations[0].expression

assert_explicit_server_only = "['https://elsewhere.example:8200']" in _expr and "'https://openbao.openbao.svc:8200'" not in _expr
assert_mount_still_derived = "['jwt-hub']" in _expr
