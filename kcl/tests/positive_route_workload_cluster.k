"""
Positive test — a route whose backend is a WORKLOAD bound to
forge.OnCluster renders a Service backendRef (ADR-0003 F2).

This is the half of the primitive that must be INDISTINGUISHABLE from the
old `service = "..."` spelling: a cluster-bound workload is reached
through its Service, so declaring the backend as a workload must render
the same backendRef it always did. If this diverged, migrating a route to
`workload` would silently change where prod traffic goes.

Also pinned here:

  * `port` is INFERRED from the workload's single declared port — the
    route does not restate it;
  * an explicit `port` on the route still wins (a workload with two
    ports, where inference is refused, must still be routable);
  * no EG Backend object is rendered for a cluster target (that is the
    host case only), and no BackendTrafficPolicy without `traffic`.
"""

import forge

_target = forge.ClusterTarget {
    cluster = "gke_acme_us-central1_prod"
    namespace = "acme-prod"
}

_gw = forge.Gateway {
    name = "public"
    host = "api.example.com"
    listeners = [
        forge.GatewayListener {
            name = "http"
            port = 80
            protocol = "HTTP"
        }
    ]
}

# One port -> the route infers it.
_api = forge.Workload {
    name = "api"
    image = "ghcr.io/acme/api"
    ports = [{name = "http", port = 8080, expose = True}]
    runtime = forge.OnCluster {target = _target}
}

# Two ports -> inference is refused (negative fixture), so this route
# states which one it means.
_multi = forge.Workload {
    name = "multi"
    image = "ghcr.io/acme/multi"
    ports = [
        {name = "http", port = 8080, expose = True}
        {name = "metrics", port = 9090, expose = True}
    ]
    runtime = forge.OnCluster {target = _target}
}

_route_inferred = forge.HTTPRoute {
    name = "api-route"
    gateway = "public"
    listener = "http"
    workload = "api"
    path = "/v1"
}

_route_explicit = forge.HTTPRoute {
    name = "multi-route"
    gateway = "public"
    listener = "http"
    workload = "multi"
    port = 9090
    path = "/metrics"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [_api, _multi]
    gateways = [_gw]
    http_routes = [_route_inferred, _route_explicit]
}

_manifests = forge.render(_bundle).manifests
_routes = [m for m in _manifests if m.kind == "HTTPRoute"]
_inferred = [m for m in _routes if m.metadata.name == "api-route"][0]
_explicit = [m for m in _routes if m.metadata.name == "multi-route"][0]

_inferred_backend = _inferred.spec.rules[0].backendRefs[0]
_explicit_backend = _explicit.spec.rules[0].backendRefs[0]

# ── A cluster workload is reached by its Service, on the inferred port ──
assert_inferred_backend_name = _inferred_backend.name == "api"
assert_inferred_backend_port = _inferred_backend.port == 8080

# No `kind` — Gateway API defaults a backendRef to Service, and stating
# it would be a difference from the `service = "..."` render for no gain.
assert_inferred_backend_is_service = "kind" not in _inferred_backend

# ── An explicit port wins over inference ───────────────────────────────
assert_explicit_backend_name = _explicit_backend.name == "multi"
assert_explicit_backend_port = _explicit_backend.port == 9090

# ── A cluster target renders NO EG Backend and NO policy ───────────────
assert_no_eg_backend = len([m for m in _manifests if m.kind == "Backend"]) == 0
assert_no_traffic_policy = len([m for m in _manifests if m.kind == "BackendTrafficPolicy"]) == 0

# ── The JSON projection carries what was DECLARED ──────────────────────
# `workload` stays a name and an inferred `port` stays absent: the
# resolved backend lives in output.manifests, which is the one authority.
_out = forge.render(_bundle)
_json_inferred = [r for r in _out.http_routes if r.name == "api-route"][0]
assert_json_workload = _json_inferred.workload == "api"
assert_json_service_absent = _json_inferred.service == Undefined or _json_inferred.service == None
assert_json_port_not_resolved = _json_inferred.port == Undefined or _json_inferred.port == None
