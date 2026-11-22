"""
Negative test — retry attempts outside 1..10.

More than ten retries of a failing backend is a retry storm, not
resilience: it multiplies load against the thing that is already failing.
"""

# expect: attempts must be 1..10

import forge

_r = forge.HTTPRoute {
    name = "api"
    gateway = "public"
    listener = "http"
    service = "api"
    port = 8080
    traffic = forge.RouteTraffic {
        retries = forge.RouteRetries {
            on = ["5xx"]
            attempts = 50
        }
    }
}
