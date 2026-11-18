"""
Negative test — a traffic duration with no unit.

Envoy rejects a bare number, so `timeout = "3600"` renders cleanly and
then fails at APPLY time naming the CRD field rather than the line that
was written. Refuse it here, where the message can name the field.
"""

# expect: is not a duration
# expect: it needs a unit

import forge

_r = forge.HTTPRoute {
    name = "api"
    gateway = "public"
    listener = "http"
    service = "api"
    port = 8080
    traffic = forge.RouteTraffic {
        timeout = "3600"
    }
}
