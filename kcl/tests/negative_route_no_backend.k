"""
Negative test — a route declares neither `service` nor `workload`.

Before ADR-0003 F2 `service` was required outright. Now that there are
two spellings, "neither" has to be refused explicitly, or a route with no
backend renders a manifest whose backendRefs name the empty string —
accepted by the API server and 503 at runtime.
"""

# expect: declares no backend
# expect: forge.Workload name

import forge

_r = forge.HTTPRoute {
    name = "api"
    gateway = "public"
    listener = "http"
    port = 8080
}
