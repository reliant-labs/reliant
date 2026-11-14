"""
Positive test — a route whose backend is a workload bound to
forge.OnHost, on a LOCAL k3d env, renders an Envoy Gateway `Backend`
pointing at the host process (ADR-0003 F2).

This is the half of the primitive that did not exist before. The Docker
daemon reaches only Docker-published localhost ports over plain HTTP, so
a dev registry sits behind the k3d gateway listener — and the token realm
it points clients at (`/auth/token`, served by control-plane's
admin-server, a HOST process in dev) has to be routed from that SAME
listener. One declaration, resolved per env.

WHY A `Backend` CRD AND NOT AN ExternalName SERVICE: Envoy Gateway
refuses an ExternalName Service as a backendRef outright — "Service ...
is of type ExternalName, which is not supported as a backend; use an
Envoy Gateway Backend resource with an FQDN endpoint instead" (verified
against EG v1.9.2 in the F2 step-1 probe, where the route went
ResolvedRefs=False and the gateway returned 500).

Nothing machine-specific is rendered: the hostname is the cluster
target's declared `host_gateway` and the port is the workload's own
`listen_ports` entry (which comes from fp.allocate_port, never a
literal).
"""

import forge

# A LOCAL k3d target — `k3d-` is the whole signal that the host is
# reachable, and it is k3d's own kubectl-context convention.
_dev = forge.ClusterTarget {
    cluster = "k3d-control-plane"
    namespace = "cp-dev"
}

_gw = forge.Gateway {
    name = "dev-public"
    listeners = [
        forge.GatewayListener {
            name = "http"
            port = 8080
            protocol = "HTTP"
        }
    ]
}

# admin-server runs as a HOST process in dev and serves the registry
# token realm. Its single listen_port is what the route reaches.
_admin = forge.Workload {
    name = "admin-server"
    runtime = forge.OnHost {
        runner = "air"
        air_config = ".air.toml"
        listen_ports = [18080]
    }
}

_realm_route = forge.HTTPRoute {
    name = "registry-realm"
    gateway = "dev-public"
    listener = "http"
    workload = "admin-server"
    path = "/auth/token"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _dev
    workloads = [_admin]
    gateways = [_gw]
    http_routes = [_realm_route]
}

_manifests = forge.render(_bundle).manifests
_route = [m for m in _manifests if m.kind == "HTTPRoute"][0]
_backends = [m for m in _manifests if m.kind == "Backend"]
_backend = _backends[0]
_ref = _route.spec.rules[0].backendRefs[0]

# ── The route's backendRef names the EG Backend, by group+kind ─────────
assert_ref_group = _ref.group == "gateway.envoyproxy.io"
assert_ref_kind = _ref.kind == "Backend"
assert_ref_name = _ref.name == "registry-realm-host"

# The port lives on the Backend's fqdn endpoint, not on the ref.
assert_ref_has_no_port = "port" not in _ref

# ── Exactly ONE Backend, and it is the EG one ──────────────────────────
assert_one_backend = len(_backends) == 1
assert_backend_api = _backend.apiVersion == "gateway.envoyproxy.io/v1alpha1"
assert_backend_name = _backend.metadata.name == "registry-realm-host"
assert_backend_ns = _backend.metadata.namespace == "cp-dev"

# ── The endpoint: the cluster's host gateway at the ALLOCATED port ─────
_fqdn = _backend.spec.endpoints[0].fqdn
assert_backend_hostname = _fqdn.hostname == "host.k3d.internal"
assert_backend_port = _fqdn.port == 18080

# ── The route still renders normally otherwise ─────────────────────────
assert_route_parent = _route.spec.parentRefs[0].name == "dev-public"
assert_route_section = _route.spec.parentRefs[0].sectionName == "http"
assert_route_path = _route.spec.rules[0].matches[0].path.value == "/auth/token"

# ── No traffic policy unless `traffic` is declared ─────────────────────
assert_no_policy = len([m for m in _manifests if m.kind == "BackendTrafficPolicy"]) == 0

# ── The JSON projection keeps the declared workload name ───────────────
_out = forge.render(_bundle)
assert_json_workload = _out.http_routes[0].workload == "admin-server"
