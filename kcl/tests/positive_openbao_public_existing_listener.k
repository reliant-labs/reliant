"""
Positive test — a Gateway that already declares an HTTPS listener for the public
host keeps it exactly as declared: forge adds no listener and no Certificate of
its own, and the route attaches to the author's listener by its name. A Gateway
whose TLS is a GKE certificate map needs no certificate of ours either.
"""

# kcl-args: env=prod

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _hub
    gateways = [forge.Gateway {
        name = "edge"
        listeners = [forge.GatewayListener {
            name = "https-secrets"
            port = 443
            protocol = "HTTPS"
            hostname = "secrets.example.com"
            tls = forge.GatewayTLS {cert_issuer = "byo-issuer", secret_name = "secrets-byo-tls"}
        }]
    }]
    openbao = forge.OpenBao {public = forge.OpenBaoPublic {host = "secrets.example.com", gateway = "edge"}}
})

_ob = [m for m in output.manifests if m.kind == "OpenBao"][0]
_gw = [m for m in output.manifests if m.kind == "Gateway" and m.metadata.name == "edge"][0]
_certs = [m for m in output.manifests if m.kind == "Certificate"]

assert_no_second_listener = len(_gw.spec.listeners) == 1 and _gw.spec.listeners[0].name == "https-secrets"
assert_route_uses_the_authors_listener = _ob.spec.public.listener == "https-secrets"
assert_authors_certificate_only = len(_certs) == 1 and _certs[0].spec.secretName == "secrets-byo-tls"
