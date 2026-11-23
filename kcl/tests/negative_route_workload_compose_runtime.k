"""
Negative test — a route targets a workload bound to forge.OnCompose.

A compose service has no in-cluster address and no host gateway entry a
Gateway can route to; it publishes ports on the host. The refusal says so
and names the fix (bind it OnHost) rather than leaving the author to
infer why a legal-looking declaration produced nothing.
"""

# expect: whose runtime is compose
# expect: no address a Gateway can route to

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

_nats = forge.Workload {
    name = "nats"
    runtime = forge.OnCompose {}
}

_r = forge.HTTPRoute {
    name = "nats-route"
    gateway = "public"
    listener = "http"
    workload = "nats"
    port = 4222
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _dev
    workloads = [_nats]
    gateways = [_gw]
    http_routes = [_r]
}

manifests = forge.render(_bundle).manifests
