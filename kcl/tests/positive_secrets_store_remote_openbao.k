"""
Positive test — an OpenBao published on a Gateway is reached over its public
host from another cluster: the audience hangs off the public host, the CA is
the system roots (an issued certificate), and each cluster gets its own mount.
Two namespaces get two login identities; one namespace with two sources
shares one SecretStore.
"""

# kcl-args: env=prod

import forge

_hub = forge.ClusterTarget {cluster = "gke_hub", namespace = "hub", platform = "amd64", connected_cluster = "hub"}
_edge = forge.ClusterTarget {cluster = "gke_edge", namespace = "edge", platform = "amd64", connected_cluster = "edge"}

_out = forge.render(forge.Bundle {
    project = "fixture"
    cluster_target = _hub
    gateways = [forge.Gateway {
        name = "public"
        listeners = [forge.GatewayListener {name = "https", port = 443, protocol = "HTTPS", hostname = "secrets.example.com", tls = forge.GatewayTLS {cert_issuer = "letsencrypt", secret_name = "secrets-tls"}}]
    }]
    openbao = forge.OpenBao {public = forge.OpenBaoPublic {host = "secrets.example.com", gateway = "public"}}
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub}, forge.ExternalSecretsOperator {cluster = _edge}]
    required_secrets = [
        forge.ExternalSecret {name = "a", namespace = "edge", keys = ["k"], source = forge.StoreSource {store = forge.OpenBaoStore {}, data = {"k" = "A"}, cluster = "gke_edge"}}
        forge.ExternalSecret {name = "b", namespace = "edge", keys = ["k"], source = forge.StoreSource {store = forge.OpenBaoStore {}, data = {"k" = "B"}, cluster = "gke_edge"}}
        forge.ExternalSecret {name = "c", namespace = "hub", keys = ["k"], source = forge.StoreSource {store = forge.OpenBaoStore {}, data = {"k" = "C"}}}
    ]
})

_ss = [x for x in _out.manifests if x.kind == "SecretStore"]
_edge_ss = [x for x in _ss if x.metadata.namespace == "edge"]
_hub_ss = [x for x in _ss if x.metadata.namespace == "hub"]

assert_one_store_per_namespace = len(_edge_ss) == 1 and len(_hub_ss) == 1
assert_remote_uses_the_public_host = _edge_ss[0].spec.provider.vault.server == "https://secrets.example.com" and _edge_ss[0].spec.provider.vault.auth.jwt.kubernetesServiceAccountToken.audiences == ["https://secrets.example.com/c/edge"]
assert_remote_trusts_system_roots = "caProvider" not in _edge_ss[0].spec.provider.vault
assert_local_uses_the_service = _hub_ss[0].spec.provider.vault.server == "https://openbao.openbao.svc:8200" and _hub_ss[0].spec.provider.vault.caProvider.name == "openbao-ca"
assert_each_cluster_has_its_own_mount = _edge_ss[0].spec.provider.vault.auth.jwt.path == "jwt-edge" and _hub_ss[0].spec.provider.vault.auth.jwt.path == "jwt-hub"
assert_roles_differ = _edge_ss[0].spec.provider.vault.auth.jwt.role != _hub_ss[0].spec.provider.vault.auth.jwt.role
assert_each_object_routes_to_its_cluster = all x in _out.manifests { x.kind not in ["SecretStore", "ExternalSecret", "ServiceAccount", "Role", "RoleBinding"] or x.metadata.labels["forge.dev/cluster"] == ("gke_edge" if x.metadata.namespace == "edge" else "gke_hub") }
assert_login_identity_per_namespace = len([x for x in _out.manifests if x.kind == "ServiceAccount" and x.metadata.name == "reliant-secrets"]) == 2
