"""
Negative test — a public host with no certificate to serve is refused, naming
both ways to give it one.
"""

# expect: needs a certificate
# expect: forge.OpenBaoPublic

import forge

_hub = forge.ClusterTarget {cluster = "gke_hub", namespace = "hub", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = _hub
    gateways = [forge.Gateway {
        name = "public"
        listeners = [forge.GatewayListener {name = "http", port = 80, protocol = "HTTP"}]
    }]
    openbao = forge.OpenBao {public = forge.OpenBaoPublic {host = "secrets.example.com", gateway = "public"}}
})
