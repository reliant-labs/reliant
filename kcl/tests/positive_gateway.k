"""
Positive tests — Gateway API ingress topology.

Exercises:

  * A Gateway with multiple listeners (HTTP + H2C for HTTP/gRPC split).
  * An HTTPRoute and GRPCRoute attached to the gateway.
  * The Bundle JSON projection includes `gateways` / `http_routes` /
    `grpc_routes` arrays.
  * Optional fields project as null (not omitted) so the consumer sees
    stable keys.
  * GRPCRoute on an H2C listener is valid (the gRPC-on-cleartext path).
"""

import forge

_public = forge.Gateway {
    name = "public"
    gateway_class_name = "traefik"
    listeners = [
        forge.GatewayListener {
            name = "http"
            port = 18080
            protocol = "HTTP"
        }
        forge.GatewayListener {
            name = "grpc"
            port = 19190
            protocol = "H2C"
        }
    ]
}

_route_http = forge.HTTPRoute {
    name = "workspace-proxy"
    gateway = "public"
    listener = "http"
    service = "workspace-proxy"
    port = 8080
}

_route_grpc = forge.GRPCRoute {
    name = "daemon-gateway"
    gateway = "public"
    listener = "grpc"
    service = "daemon-gateway"
    port = 19190
}

_bundle = forge.Bundle {
    project = "fixture"
    gateways = [_public]
    http_routes = [_route_http]
    grpc_routes = [_route_grpc]
}

output = forge.render(_bundle)

# ── Invariants ─────────────────────────────────────────────────────

# Bundle exposes the three Gateway API arrays.
assert_gateways_count = len(output.gateways) == 1
assert_http_routes_count = len(output.http_routes) == 1
assert_grpc_routes_count = len(output.grpc_routes) == 1

# Gateway projects its required + default fields.
assert_gw_name = output.gateways[0].name == "public"
assert_gw_class = output.gateways[0].gateway_class_name == "traefik"
assert_gw_tls_null = output.gateways[0].tls == None
assert_gw_host_null = output.gateways[0].host == None

# Listeners project in declaration order with all fields populated.
assert_gw_listeners_count = len(output.gateways[0].listeners) == 2
assert_gw_listener_0_name = output.gateways[0].listeners[0].name == "http"
assert_gw_listener_0_protocol = output.gateways[0].listeners[0].protocol == "HTTP"
assert_gw_listener_1_protocol = output.gateways[0].listeners[1].protocol == "H2C"

# Routes carry their gateway+listener references.
assert_http_route_gw = output.http_routes[0].gateway == "public"
assert_http_route_listener = output.http_routes[0].listener == "http"
assert_http_route_service = output.http_routes[0].service == "workspace-proxy"
assert_grpc_route_listener = output.grpc_routes[0].listener == "grpc"
