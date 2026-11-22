"""
Negative test — a route targets an OnHost workload while landing on a
cluster that is NOT a local k3d one.

A host process is reachable from a cluster running on the same machine,
via that cluster's host gateway, and from nowhere else. On a cloud
cluster `host.k3d.internal` resolves to nothing — so rendering this would
produce a route that applies cleanly and then 503s with no config error
to point at, which is the expensive kind of failure.
"""

# expect: runs on the HOST
# expect: is not a local k3d cluster

import forge

_prod = forge.ClusterTarget {
    cluster = "gke_acme_us-central1_prod"
    namespace = "acme-prod"
}

_gw = forge.Gateway {
    name = "public"
    host = "api.example.com"
    listeners = [
        forge.GatewayListener {
            name = "http"
            port = 80
            protocol = "HTTP"
        }
    ]
}

_admin = forge.Workload {
    name = "admin-server"
    # `command` so the host runtime is complete on its own — the go-run
    # runner would otherwise demand a forge.GoBuild, which is a different
    # rule than the one under test.
    command = ["./bin/admin-server"]
    runtime = forge.OnHost {runner = "binary", listen_ports = [18080]}
}

_r = forge.HTTPRoute {
    name = "realm"
    gateway = "public"
    listener = "http"
    workload = "admin-server"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _prod
    workloads = [_admin]
    gateways = [_gw]
    http_routes = [_r]
}

manifests = forge.render(_bundle).manifests
