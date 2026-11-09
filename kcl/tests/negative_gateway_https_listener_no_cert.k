"""
Negative test — an HTTPS listener that resolves to NO cert material is
rejected at render.

Neither the listener nor the gateway declares `tls`, so the listener
would render with no certificateRefs. The Gateway controller ACCEPTS
that manifest and then cannot serve it: TLS handshakes fail at runtime
with no config error to point at. Fail loudly here instead.
"""

# expect: HTTPS listener(s) [https] resolve to no TLS config

import forge

_gw = forge.Gateway {
    name = "public"
    host = "api.example.com"
    listeners = [
        forge.GatewayListener {
            name = "https"
            port = 443
            protocol = "HTTPS"
        }
    ]
}

out = _gw.name
