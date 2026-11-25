"""
Closed-schema test — `raw_policy` is GONE from Gateway too (ADR-0003 F2).

Same reasoning as the HTTPRoute case: the field was dead on all three
ingress kinds (Gateway, HTTPRoute, GRPCRoute), so all three lose it in
the same change rather than leaving one plausible-looking survivor.
"""

# expect: raw_policy

import forge

_gw = forge.Gateway {
    name = "public"
    listeners = [
        forge.GatewayListener {
            name = "http"
            port = 80
            protocol = "HTTP"
        }
    ]
    raw_policy = "kind: ClientTrafficPolicy\n"
}
