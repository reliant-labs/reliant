"""
Positive test — gke_certmap-mode Gateway terminates TLS via a GCP
Certificate Manager map, NOT cert-manager. forge stamps the
`networking.gke.io/certmap` annotation on the Gateway and renders the
HTTPS listener with NO tls block (no certificateRefs, no mode) — the
certmap supplies the cert. No cert-manager Certificate is emitted and no
Secret is referenced. A certmap Gateway typically serves many hostnames
via its attached routes, so it has no single `host`.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "gke-example"
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
        # gke_certmap Gateway — no host (serves many hostnames via routes);
        # should emit Gateway with certmap annotation, no Certificate, and
        # an HTTPS listener with no tls block.
        forge.Gateway {
            name = "public"
            listeners = [
                forge.GatewayListener {
                    name = "https"
                    port = 443
                    protocol = "HTTPS"
                }
            ]
            tls = forge.GatewayTLS {
                mode = "gke_certmap"
                certmap = "my-map"
            }
        }
    ]
}

manifests = forge.render(_bundle).manifests

_gateways = [m for m in manifests if m.kind == "Gateway"]
_certs = [m for m in manifests if m.kind == "Certificate"]

# JSON contract projection — render(bundle) emits the TLS block with mode.
contract = forge.render(_bundle)
_tls = contract.gateways[0].tls

# ── Invariants ─────────────────────────────────────────────────────

# Gateway resource present, NO Certificate (the certmap owns the cert).
assert_gw_count = len(_gateways) == 1
assert_no_cert = len(_certs) == 0

# Gateway carries the GKE certmap annotation pointing at the named map.
_gw = _gateways[0]
assert_certmap_annotation = _gw.metadata.annotations["networking.gke.io/certmap"] == "my-map"

# The HTTPS listener has NO tls block — the certmap supplies the cert, so
# there are no certificateRefs and no terminate mode on the listener.
_listener = _gw.spec.listeners[0]
assert_listener_proto = _listener.protocol == "HTTPS"
assert_listener_no_tls = not _listener.get("tls")

# A certmap Gateway with no `host` renders a listener with no `hostname`
# (the attached routes' hostnames select the cert via the map).
assert_listener_no_hostname = not _listener.get("hostname")

# JSON contract surfaces the mode + certmap so audit / doctor tooling can
# distinguish certmap Gateways without re-deriving.
assert_tls_mode = _tls.mode == "gke_certmap"
assert_tls_certmap = _tls.certmap == "my-map"
# secret_name / cert_issuer project as null (KCL None → JSON null) — both
# are unused for gke_certmap.
assert_tls_no_secret = _tls.secret_name == None
assert_tls_no_issuer = _tls.cert_issuer == None
