"""
Negative test — a health-check expected status outside 100..599.

`expected_statuses` is the field most likely to be typed by hand (the
registry case needs a literal 401), so a value that is not an HTTP status
at all is worth catching before the CRD does.
"""

# expect: expected_statuses entries must be 100..599

import forge

_r = forge.HTTPRoute {
    name = "api"
    gateway = "public"
    listener = "http"
    service = "api"
    port = 8080
    traffic = forge.RouteTraffic {
        health_check = forge.RouteHealthCheck {
            path = "/v2/"
            expected_statuses = [401, 4001]
        }
    }
}
