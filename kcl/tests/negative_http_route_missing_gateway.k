"""
Negative test — HTTPRoute must reference a parent Gateway by name.
Routes that omit `gateway` would be orphaned and unreachable.
"""

# expect: HTTPRoute.gateway is required

import forge

_r = forge.HTTPRoute {
    name = "api"
    gateway = ""
    listener = "http"
    service = "api"
    port = 8080
}
