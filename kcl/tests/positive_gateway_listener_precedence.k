"""
Positive test — hostname/TLS precedence and the shared-secret
(multi-SAN) certificate shape.

Two things are pinned here.

FALLBACK + PRECEDENCE. A listener that declares no `hostname`/`tls`
inherits the gateway's, which is what keeps the common single-host
Gateway a two-line declaration. Route hostname resolution is
`route.host` > `listener.hostname` > `gateway.host`; the listener sits
in the middle because it is what the LB actually serves on that
section, so a route inheriting the GATEWAY host over its own listener's
would render an HTTPRoute Gateway API drops as unattachable.

CERTIFICATE GROUPING. Certificates group by SECRET, not by listener.
Two listeners pointing at the SAME secret_name share one multi-SAN
Certificate (one Secret holds one cert, so exactly one Certificate must
fill it); different secret_names get independent single-SAN certs. That
makes multi-SAN vs per-listener a consequence of a choice the consumer
was already making, with no extra knob.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "k3d-example"
    namespace = "example-prod"
}

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
    ]
    gateways = [
        # Gateway-level host + tls act as DEFAULTS. The "inherit"
        # listener takes both; "own" overrides both.
        forge.Gateway {
            name = "public"
            host = "default.example.com"
            tls = forge.GatewayTLS {
                cert_issuer = "letsencrypt-prod"
                secret_name = "default-tls"
            }
            listeners = [
                forge.GatewayListener {name = "inherit", port = 443, protocol = "HTTPS"}
                forge.GatewayListener {
                    name = "own"
                    port = 8443
                    protocol = "HTTPS"
                    hostname = "own.example.com"
                    tls = forge.GatewayTLS {
                        cert_issuer = "letsencrypt-prod"
                        secret_name = "own-tls"
                    }
                }
            ]
        }
        # Two listeners, two hostnames, ONE shared secret -> ONE
        # multi-SAN Certificate carrying both SANs.
        forge.Gateway {
            name = "shared"
            listeners = [
                forge.GatewayListener {
                    name = "one"
                    port = 443
                    protocol = "HTTPS"
                    hostname = "one.example.com"
                    tls = forge.GatewayTLS {
                        cert_issuer = "letsencrypt-prod"
                        secret_name = "shared-tls"
                    }
                }
                forge.GatewayListener {
                    name = "two"
                    port = 8443
                    protocol = "HTTPS"
                    hostname = "two.example.com"
                    tls = forge.GatewayTLS {
                        cert_issuer = "letsencrypt-prod"
                        secret_name = "shared-tls"
                    }
                }
            ]
        }
    ]
    http_routes = [
        # No host override -> takes its LISTENER's hostname, NOT the
        # gateway default. This is the precedence regression guard.
        forge.HTTPRoute {
            name = "on-own"
            gateway = "public"
            listener = "own"
            service = "api"
            port = 8080
        }
        # No host override, listener has no hostname -> gateway default.
        forge.HTTPRoute {
            name = "on-inherit"
            gateway = "public"
            listener = "inherit"
            service = "api"
            port = 8080
        }
        # Explicit route host beats BOTH.
        forge.HTTPRoute {
            name = "override"
            gateway = "public"
            listener = "own"
            service = "api"
            port = 8080
            host = "route.example.com"
        }
    ]
}

manifests = forge.render(_bundle).manifests

_gateways = {m.metadata.name: m for m in manifests if m.kind == "Gateway"}
_certs = [m for m in manifests if m.kind == "Certificate"]
_routes = {m.metadata.name: m for m in manifests if m.kind == "HTTPRoute"}

_public = _gateways["public"]
_pl = {l.name: l for l in _public.spec.listeners}

# ── Fallback: unset listener inherits the gateway's host + tls ─────
assert_inherit_hostname = _pl["inherit"].hostname == "default.example.com"
assert_inherit_secret = _pl["inherit"].tls.certificateRefs[0].name == "default-tls"

# ── Override: listener values win over the gateway's ───────────────
assert_own_hostname = _pl["own"].hostname == "own.example.com"
assert_own_secret = _pl["own"].tls.certificateRefs[0].name == "own-tls"

# ── Route host precedence ──────────────────────────────────────────
# listener.hostname beats gateway.host ...
assert_route_takes_listener_host = _routes["on-own"].spec.hostnames == ["own.example.com"]
# ... gateway.host is the fallback when the listener declares none ...
assert_route_falls_back_to_gateway = _routes["on-inherit"].spec.hostnames == ["default.example.com"]
# ... and an explicit route host beats both.
assert_route_override_wins = _routes["override"].spec.hostnames == ["route.example.com"]

# ── Certificate grouping is by SECRET ──────────────────────────────
_by_secret = {c.spec.secretName: c for c in _certs}

# public: two distinct secrets -> two single-SAN certs. The one using
# the GATEWAY's secret keeps the bare "<gateway>-tls" name.
assert_public_default_cert_name = _by_secret["default-tls"].metadata.name == "public-tls"
assert_public_default_san = _by_secret["default-tls"].spec.dnsNames == ["default.example.com"]
assert_public_own_cert_name = _by_secret["own-tls"].metadata.name == "public-own-tls"
assert_public_own_san = _by_secret["own-tls"].spec.dnsNames == ["own.example.com"]

# shared: one secret across two listeners -> ONE multi-SAN cert.
assert_shared_cert_count = len([c for c in _certs if c.spec.secretName == "shared-tls"]) == 1
assert_shared_multi_san = _by_secret["shared-tls"].spec.dnsNames == ["one.example.com", "two.example.com"]

# Three secrets total, three Certificates — no Secret double-filled
# (two Certificates on one Secret race parallel ACME orders).
assert_total_cert_count = len(_certs) == 3
assert_one_cert_per_secret = len({c.spec.secretName: None for c in _certs}) == len(_certs)
