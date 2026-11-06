"""
Negative test — a workload-backed route omits `port` while the target
exposes more than one.

Inference is only safe when it is unambiguous. Picking the first port
would route traffic to whichever one happened to be declared first, and
"first in the list" is not a decision anyone made.
"""

# expect: ports
# expect: cannot be inferred

import forge

_dev = forge.ClusterTarget {
    cluster = "k3d-control-plane"
    namespace = "cp-dev"
}

_gw = forge.Gateway {
    name = "public"
    listeners = [
        forge.GatewayListener {
            name = "http"
            port = 8080
            protocol = "HTTP"
        }
    ]
}

_api = forge.Workload {
    name = "api"
    image = "ghcr.io/acme/api"
    ports = [
        {name = "http", port = 8080, expose = True}
        {name = "metrics", port = 9090, expose = True}
    ]
    runtime = forge.OnCluster {target = _dev}
}

_r = forge.HTTPRoute {
    name = "api-route"
    gateway = "public"
    listener = "http"
    workload = "api"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _dev
    workloads = [_api]
    gateways = [_gw]
    http_routes = [_r]
}

manifests = forge.render(_bundle).manifests
