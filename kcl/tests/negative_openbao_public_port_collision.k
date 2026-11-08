"""
Negative test — the Gateway's port must not be one of the store's own
listeners: X-Forwarded-For is trusted on it and on no other.
"""

# expect: is also a listener of the store
# expect: only it is trusted to set X-Forwarded-For

import forge

_hub = forge.ClusterTarget {cluster = "gke_hub", namespace = "hub", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = _hub
    gateways = [forge.Gateway {
        name = "public"
        tls = forge.GatewayTLS {cert_issuer = "le", secret_name = "public-tls"}
        listeners = [forge.GatewayListener {name = "http", port = 80, protocol = "HTTP"}]
    }]
    openbao = forge.OpenBao {public = forge.OpenBaoPublic {host = "secrets.example.com", gateway = "public", port = 8200}}
})
