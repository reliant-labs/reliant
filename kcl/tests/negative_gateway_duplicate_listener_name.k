"""
Negative test — two listeners on a Gateway sharing the same name
fail the schema check. Listener names map to Gateway API
spec.listeners[*].name and must be unique within a gateway since
routes attach via sectionName.
"""

# expect: Gateway.listeners[*].name must be unique within a gateway

import forge

_gw = forge.Gateway {
    name = "public"
    listeners = [
        forge.GatewayListener {
            name = "http"
            port = 18080
            protocol = "HTTP"
        }
        forge.GatewayListener {
            name = "http"
            port = 18180
            protocol = "HTTP"
        }
    ]
}
