"""
Negative test — two listeners identical on (port, protocol, hostname)
fail the schema check.

Listener identity in Gateway API is the TRIPLE, not the port alone.
Two listeners may share a port when they differ on protocol or on
hostname — that is how one Gateway (one load balancer, one IP) serves
many hosts by SNI, and positive_gateway_listener_precedence.k covers
it. What stays illegal is two listeners the Gateway controller cannot
tell apart, which is what this fixture asserts.
"""

# expect: must be unique on (port, protocol, hostname)

import forge

_gw = forge.Gateway {
    name = "public"
    host = "api.example.com"
    tls = forge.GatewayTLS {
        mode = "mkcert"
        secret_name = "api-tls"
    }
    listeners = [
        forge.GatewayListener {
            name = "https-a"
            port = 18443
            protocol = "HTTPS"
            hostname = "api.example.com"
        }
        forge.GatewayListener {
            name = "https-b"
            port = 18443
            protocol = "HTTPS"
            hostname = "api.example.com"
        }
    ]
}
