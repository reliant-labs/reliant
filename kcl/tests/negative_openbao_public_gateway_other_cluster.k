"""
Negative test — a public Gateway on another cluster than the store is refused:
the route reaches OpenBao through its Service, which exists on one cluster.
"""

# expect: lands on cluster 'gke_edge', but OpenBao runs on 'gke_hub'
# expect: forge.Gateway.cluster

import forge

_hub = forge.ClusterTarget {cluster = "gke_hub", namespace = "hub", platform = "amd64", connected_cluster = "hub"}
_edge = forge.ClusterTarget {cluster = "gke_edge", namespace = "edge", platform = "amd64", connected_cluster = "edge"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "prod"
    cluster_target = _edge
    gateways = [forge.Gateway {
        name = "public"
        tls = forge.GatewayTLS {cert_issuer = "le", secret_name = "public-tls"}
        listeners = [forge.GatewayListener {name = "http", port = 80, protocol = "HTTP"}]
    }]
    openbao = forge.OpenBao {
        runtime = forge.OnCluster {target = _hub}
        public = forge.OpenBaoPublic {host = "secrets.example.com", gateway = "public"}
    }
})
