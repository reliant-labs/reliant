"""
Negative test — a route sets BOTH `service` and `workload`.

A route has one backend. Accepting both would mean silently picking one,
and whichever was picked would be right half the time: the author who
wrote both meant the one the renderer ignored as often as not.
"""

# expect: sets BOTH `service`
# expect: a route has ONE backend

import forge

_r = forge.HTTPRoute {
    name = "api"
    gateway = "public"
    listener = "http"
    service = "api-svc"
    workload = "api"
    port = 8080
}
