"""
Negative test — listener.path_prefix must start with `/` when set.
A bare path like "cp-forge-dev-a" instead of "/cp-forge-dev-a"
would slip through to the route's effective path and fail at
kubectl apply.
"""

# expect: GatewayListener.path_prefix must start with '/'

import forge

_gw = forge.Gateway {
    name = "public"
    listeners = [
        forge.GatewayListener {
            name = "http"
            port = 18080
            protocol = "HTTP"
            path_prefix = "cp-forge-dev-a"
        }
    ]
}
