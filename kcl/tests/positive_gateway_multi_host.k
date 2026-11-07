"""
Positive test — ONE Gateway serves MANY hostnames via per-listener
`hostname` + `tls`.

This is the consolidation forge previously could not express. Modeled on
the shape a real consumer needs in prod: four public hostnames, one of
them a wildcard, one of them carrying native gRPC — which previously
forced FOUR Gateways, and therefore four cloud load balancers, four Envoy
deployments and four IPs. Here it is one Gateway with four listeners.

Invariants asserted:

  * ONE Gateway resource, with one listener per hostname, each carrying
    its OWN `hostname` and its own certificateRefs Secret.
  * Certificates cover EVERY host — one Certificate per distinct Secret.
  * Routes (HTTPRoute and GRPCRoute) resolve their hostname from the
    LISTENER they attach to, not from the gateway default.
  * A plaintext HTTP listener stays hostname-less (it serves all hosts,
    e.g. an ACME HTTP-01 solver route) and carries no tls block.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "k3d-example"
    namespace = "example-prod"
}

_issuer = "letsencrypt-dns01"

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [
        fw.Workload {
            name = "api"
            image = "localhost:5050/example"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _target}
        }
        fw.Workload {
            name = "admin"
            image = "localhost:5050/example"
            ports = [fw.Port {name = "http", port = 8081}]
            runtime = forge.OnCluster {target = _target}
        }
        fw.Workload {
            name = "daemon-gateway"
            image = "localhost:5050/example"
            ports = [fw.Port {name = "http", port = 9190}]
            runtime = forge.OnCluster {target = _target}
        }
    ]
    gateways = [
        forge.Gateway {
            name = "public"
            # NO gateway-level host or tls: every HTTPS listener is
            # self-sufficient. This is the multi-hostname shape.
            listeners = [
                # Plaintext solver listener — no hostname, no TLS.
                forge.GatewayListener {name = "http", port = 80, protocol = "HTTP"}
                forge.GatewayListener {
                    name = "api"
                    port = 443
                    protocol = "HTTPS"
                    hostname = "api.example.com"
                    tls = forge.GatewayTLS {
                        cert_issuer = _issuer
                        secret_name = "api-tls"
                    }
                }
                forge.GatewayListener {
                    name = "admin"
                    port = 8443
                    protocol = "HTTPS"
                    hostname = "admin.example.com"
                    tls = forge.GatewayTLS {
                        cert_issuer = _issuer
                        secret_name = "admin-tls"
                    }
                }
                # Wildcard SAN — needs a DNS-01 solver, which is exactly
                # why it gets its OWN Secret/Certificate rather than
                # being folded into a shared multi-SAN cert.
                forge.GatewayListener {
                    name = "workspaces"
                    port = 9443
                    protocol = "HTTPS"
                    hostname = "*.workspaces.example.com"
                    tls = forge.GatewayTLS {
                        cert_issuer = _issuer
                        secret_name = "workspaces-tls"
                    }
                }
                # Native gRPC on the SAME Gateway — Envoy Gateway serves
                # GRPCRoute and HTTPRoute off one listener set.
                forge.GatewayListener {
                    name = "grpc"
                    port = 10443
                    protocol = "HTTPS"
                    hostname = "gateway.example.com"
                    tls = forge.GatewayTLS {
                        cert_issuer = _issuer
                        secret_name = "grpc-tls"
                    }
                }
            ]
        }
    ]
    http_routes = [
        forge.HTTPRoute {
            name = "api"
            gateway = "public"
            listener = "api"
            service = "api"
            port = 8080
        }
        forge.HTTPRoute {
            name = "admin"
            gateway = "public"
            listener = "admin"
            service = "admin"
            port = 8081
        }
    ]
    grpc_routes = [
        forge.GRPCRoute {
            name = "daemon-gateway"
            gateway = "public"
            listener = "grpc"
            service = "daemon-gateway"
            port = 9190
        }
    ]
}

manifests = forge.render(_bundle).manifests

_gateways = [m for m in manifests if m.kind == "Gateway"]
_certs = [m for m in manifests if m.kind == "Certificate"]
_http = [m for m in manifests if m.kind == "HTTPRoute"]
_grpc = [m for m in manifests if m.kind == "GRPCRoute"]

_gw = _gateways[0]
_l = {l.name: l for l in _gw.spec.listeners}

# ── One Gateway, five listeners ────────────────────────────────────
# THE headline invariant: four hostnames, ONE Gateway == one LB.
assert_single_gateway = len(_gateways) == 1
assert_listener_count = len(_gw.spec.listeners) == 5

# ── Each listener carries its OWN hostname ─────────────────────────
assert_api_hostname = _l["api"].hostname == "api.example.com"
assert_admin_hostname = _l["admin"].hostname == "admin.example.com"
assert_workspaces_hostname = _l["workspaces"].hostname == "*.workspaces.example.com"
assert_grpc_hostname = _l["grpc"].hostname == "gateway.example.com"

# Every HTTPS listener's hostname is DISTINCT — the whole point.
_https_hosts = [l.hostname for l in _gw.spec.listeners if l.name != "http"]
assert_hostnames_distinct = len({h: None for h in _https_hosts}) == 4

# Plaintext listener stays hostname-less and TLS-less.
assert_http_no_hostname = not _l["http"].get("hostname")
assert_http_no_tls = not _l["http"].get("tls")

# ── Each HTTPS listener terminates its OWN Secret ──────────────────
assert_api_secret = _l["api"].tls.certificateRefs[0].name == "api-tls"
assert_admin_secret = _l["admin"].tls.certificateRefs[0].name == "admin-tls"
assert_workspaces_secret = _l["workspaces"].tls.certificateRefs[0].name == "workspaces-tls"
assert_grpc_secret = _l["grpc"].tls.certificateRefs[0].name == "grpc-tls"
assert_api_terminate = _l["api"].tls.mode == "Terminate"

# ── Certificates cover EVERY host ──────────────────────────────────
# One Certificate per distinct Secret; the union of all dnsNames is
# exactly the set of HTTPS listener hostnames — no host left uncovered.
assert_cert_count = len(_certs) == 4
_covered = [d for c in _certs for d in c.spec.dnsNames]
assert_cert_covers_api = "api.example.com" in _covered
assert_cert_covers_admin = "admin.example.com" in _covered
assert_cert_covers_workspaces = "*.workspaces.example.com" in _covered
assert_cert_covers_grpc = "gateway.example.com" in _covered
assert_cert_covers_exactly = len({d: None for d in _covered}) == 4

# Each Certificate is single-SAN and named for the listener that
# introduced its Secret, so two secrets never collide on one name.
_by_secret = {c.spec.secretName: c for c in _certs}
assert_cert_api_single_san = _by_secret["api-tls"].spec.dnsNames == ["api.example.com"]
assert_cert_api_name = _by_secret["api-tls"].metadata.name == "public-api-tls"
assert_cert_ws_name = _by_secret["workspaces-tls"].metadata.name == "public-workspaces-tls"
assert_cert_names_distinct = len({c.metadata.name: None for c in _certs}) == 4
assert_cert_issuer = _by_secret["api-tls"].spec.issuerRef.name == _issuer

# ── Routes resolve host from THEIR listener ────────────────────────
# The gateway has no default host, so a route with no override must
# take the listener's hostname or it would render unattachable.
_http_by_name = {r.metadata.name: r for r in _http}
assert_http_route_count = len(_http) == 2
assert_api_route_host = _http_by_name["api"].spec.hostnames == ["api.example.com"]
assert_admin_route_host = _http_by_name["admin"].spec.hostnames == ["admin.example.com"]
assert_api_route_section = _http_by_name["api"].spec.parentRefs[0].sectionName == "api"

# GRPCRoute resolves the same way, on the same Gateway.
assert_grpc_route_count = len(_grpc) == 1
assert_grpc_route_host = _grpc[0].spec.hostnames == ["gateway.example.com"]
assert_grpc_route_parent = _grpc[0].spec.parentRefs[0].name == "public"
assert_grpc_route_section = _grpc[0].spec.parentRefs[0].sectionName == "grpc"
