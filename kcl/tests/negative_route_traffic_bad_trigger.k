"""
Negative test — a retry trigger outside Envoy Gateway's closed enum.

The CRD refuses an unknown trigger at apply time. Catching it at render
time is the difference between a message naming the field you wrote and
one naming a Kubernetes admission webhook.
"""

# expect: must each be one of

import forge

_r = forge.HTTPRoute {
    name = "api"
    gateway = "public"
    listener = "http"
    service = "api"
    port = 8080
    traffic = forge.RouteTraffic {
        retries = forge.RouteRetries {
            on = ["5xx", "always"]
            attempts = 3
        }
    }
}
