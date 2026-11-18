"""
Negative test — cert material on a PLAINTEXT listener is rejected.

An H2C/HTTP listener carrying `tls` is a silent misconfiguration: the
author believes the listener terminates TLS, the renderer drops the
block (plaintext listeners emit no tls), and the service is served
unencrypted with nothing anywhere reporting a problem. Reject it rather
than quietly ignoring it.
"""

# expect: GatewayListener.tls is only valid on an HTTPS listener

import forge

_l = forge.GatewayListener {
    name = "grpc"
    port = 9190
    protocol = "H2C"
    tls = forge.GatewayTLS {
        cert_issuer = "letsencrypt-prod"
        secret_name = "grpc-tls"
    }
}

out = _l.name
