"""
Positive test — the ESO admission lists default from the render.

An empty `admission.servers / jwt_mounts / audiences` is not a wildcard, so
policies built from empty lists would refuse the SecretStores the same Bundle
renders. They are derived instead: each rendered store's URL, JWT mount and
audience, each gcpsm store's project, and the in-cluster OpenBao component's
own server, mount and audience (admitted before any ExternalSecret reads it).
"""

# kcl-args: env=prod

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "fixture"
    cluster_target = _hub
    control_plane = forge.ControlPlane {}
    openbao = forge.OpenBao {}
    external_secrets_operators = [forge.ExternalSecretsOperator {
        cluster = _hub
        gcp_service_account = "eso@acme-prod.iam.gserviceaccount.com"
    }]
    required_secrets = [
        forge.ExternalSecret {
            name = "a"
            namespace = "acme-prod"
            keys = ["k"]
            source = forge.StoreSource {store = forge.OpenBaoStore {}, data = {"k" = "A"}}
        }
        forge.ExternalSecret {
            name = "b"
            namespace = "acme-prod"
            keys = ["k"]
            source = forge.StoreSource {store = forge.GCPSecretManagerStore {project = "acme-gsm"}, data = {"k" = "B"}}
        }
        forge.ExternalSecret {
            name = "c"
            namespace = "acme-prod"
            keys = ["k"]
            source = forge.StoreSource {store = forge.HostedStore {org = "org1", env_id = "env1"}, data = {"k" = "C"}}
        }
    ]
})

_pin = [o for o in output.helm_charts[0].values.extraObjects if o.kind == "ValidatingAdmissionPolicy" and o.metadata.name == "eso-store-pin"][0]
_expr = _pin.spec.validations[0].expression

assert_openbao_server = "'https://openbao.openbao.svc:8200'" in _expr
assert_hosted_server = "'https://secrets.reliantapi.com'" in _expr
assert_mount = "['jwt-hub']" in _expr
assert_openbao_audience = "'https://openbao.openbao.svc:8200/c/hub'" in _expr
assert_hosted_audience = "'https://secrets.reliantapi.com/c/hub'" in _expr
assert_gcp_project = "['acme-gsm']" in _expr
