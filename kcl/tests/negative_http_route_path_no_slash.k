"""
Negative test — HTTPRoute.path must start with `/` when set.
A bare segment would be interpreted as a path component without
the leading slash and produce a broken effective path.
"""

# expect: HTTPRoute.path must start with '/'

import forge

_r = forge.HTTPRoute {
    name = "api"
    gateway = "public"
    listener = "http"
    service = "api"
    port = 8080
    path = "v1"
}
