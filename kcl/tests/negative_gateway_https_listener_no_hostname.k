"""
Negative test — an HTTPS listener that resolves to NO hostname is
rejected at render.

The listener declares its own TLS but neither it nor the gateway
declares a hostname. That wedges the cluster two ways: cert-manager's
gateway-shim rejects the WHOLE Gateway ("spec.listeners[*].hostname:
Required value"), and the load balancer has no SNI key to select a
certificate with.

(The one legitimate hostname-less HTTPS shape is gke_certmap, where a
GCP Certificate Manager map supplies a cert per hostname — exempted by
the check and covered by positive_gateway_certmap.k.)
"""

# expect: HTTPS listener(s) [https] resolve to no hostname

import forge

_gw = forge.Gateway {
    name = "public"
    listeners = [
        forge.GatewayListener {
            name = "https"
            port = 443
            protocol = "HTTPS"
            tls = forge.GatewayTLS {
                cert_issuer = "letsencrypt-prod"
                secret_name = "api-tls"
            }
        }
    ]
}

out = _gw.name
