"""
Positive test — Gateway API resources render to the correct
gateway.networking.k8s.io/v1 manifests.

Asserts that output.manifests carries a Gateway with the expected
listeners and an HTTPRoute with the expected parentRefs +
backendRefs.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "k3d-example"
    namespace = "example-dev"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/example"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _target}
        }
    ]
    gateways = [
        forge.Gateway {
            name = "public"
            host = "api.example.com"
            listeners = [
                forge.GatewayListener {
                    name = "http"
                    port = 18080
                    protocol = "HTTP"
                }
            ]
        }
    ]
    http_routes = [
        forge.HTTPRoute {
            name = "api"
            gateway = "public"
            listener = "http"
            service = "api"
            port = 8080
            path = "/v1"
        }
    ]
}

manifests = forge.render(_bundle).manifests

# Find the Gateway + HTTPRoute manifests by kind.
_gateways = [m for m in manifests if m.kind == "Gateway"]
_routes = [m for m in manifests if m.kind == "HTTPRoute"]

# ── Invariants ─────────────────────────────────────────────────────

# Exactly one Gateway + one HTTPRoute rendered.
assert_gw_count = len(_gateways) == 1
assert_route_count = len(_routes) == 1

# Gateway carries the expected metadata + spec.
assert_gw_api = _gateways[0].apiVersion == "gateway.networking.k8s.io/v1"
assert_gw_name = _gateways[0].metadata.name == "public"
assert_gw_ns = _gateways[0].metadata.namespace == "example-dev"
assert_gw_class = _gateways[0].spec.gatewayClassName == "eg"
assert_gw_listener_port = _gateways[0].spec.listeners[0].port == 18080
assert_gw_listener_proto = _gateways[0].spec.listeners[0].protocol == "HTTP"

# HTTPRoute references the gateway by name + listener section.
assert_route_api = _routes[0].apiVersion == "gateway.networking.k8s.io/v1"
assert_route_parent_name = _routes[0].spec.parentRefs[0].name == "public"
assert_route_parent_section = _routes[0].spec.parentRefs[0].sectionName == "http"
assert_route_hostname = _routes[0].spec.hostnames[0] == "api.example.com"
assert_route_path_value = _routes[0].spec.rules[0].matches[0].path.value == "/v1"
assert_route_path_type = _routes[0].spec.rules[0].matches[0].path.type == "PathPrefix"
assert_route_backend_name = _routes[0].spec.rules[0].backendRefs[0].name == "api"
assert_route_backend_port = _routes[0].spec.rules[0].backendRefs[0].port == 8080
