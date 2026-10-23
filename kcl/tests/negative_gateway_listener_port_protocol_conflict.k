"""
Negative test — two listeners sharing a port but declaring DIFFERENT
protocols fail the schema check.

Sharing a port is legal and deliberate: several HTTPS listeners on :443,
one per hostname, is how one Gateway (one load balancer, one IP) serves
many hosts by SNI — positive_gateway_listener_precedence.k covers that,
and prod ingress relies on it. What is not legal is two PROTOCOLS on one
port. A bound port speaks one protocol, so this renders a manifest the
Gateway controller accepts and then marks Conflicted, serving NEITHER
listener — a status condition with no application log to point at.

This is the invariant that port-alone uniqueness used to enforce as a
side effect, before listener identity became (port, protocol, hostname).
It is now its own check, so both properties hold at once.
"""

# expect: listeners sharing a port must agree on protocol

import forge

_gw = forge.Gateway {
    name = "public"
    tls = forge.GatewayTLS {
        mode = "mkcert"
        secret_name = "api-tls"
    }
    listeners = [
        forge.GatewayListener {
            name = "plaintext"
            port = 443
            protocol = "HTTP"
        }
        forge.GatewayListener {
            name = "tls"
            port = 443
            protocol = "HTTPS"
            hostname = "api.example.com"
        }
    ]
}
