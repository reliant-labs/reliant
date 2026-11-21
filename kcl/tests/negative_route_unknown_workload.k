"""
Negative test — a route targets a workload this env does not declare.

A typo'd backend name is the cheapest possible mistake to make and one of
the more expensive to diagnose from a running cluster, so the refusal
names the workloads the env DOES have.
"""

# expect: this env declares no workload for
# expect: Workloads this env has

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
    ports = [{name = "http", port = 8080, expose = True}]
    runtime = forge.OnCluster {target = _dev}
}

_r = forge.HTTPRoute {
    name = "realm"
    gateway = "public"
    listener = "http"
    workload = "admin-sever"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _dev
    workloads = [_api]
    gateways = [_gw]
    http_routes = [_r]
}

manifests = forge.render(_bundle).manifests
