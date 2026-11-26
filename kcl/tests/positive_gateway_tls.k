"""
Positive test — Gateway with TLS emits a cert-manager Certificate
resource that fills the named Secret, and the HTTPS listener carries the
gateway host as its `hostname`. The Gateway does NOT carry a
cert-manager.io/cluster-issuer annotation: forge owns issuance via the
explicit Certificate, so stamping the annotation too would make
cert-manager's gateway-shim auto-create a second, competing Certificate.
Gateways without TLS emit no Certificate.
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
        # TLS-bearing Gateway — should emit a Certificate.
        forge.Gateway {
            name = "public"
            host = "api.example.com"
            listeners = [
                forge.GatewayListener {
                    name = "https"
                    port = 443
                    protocol = "HTTPS"
                }
            ]
            tls = forge.GatewayTLS {
                cert_issuer = "letsencrypt-prod"
                secret_name = "api-tls"
            }
        },
        # No-TLS Gateway — should NOT emit a Certificate.
        forge.Gateway {
            name = "internal"
            host = "internal.example.com"
            listeners = [
                forge.GatewayListener {
                    name = "http"
                    port = 18081
                    protocol = "HTTP"
                }
            ]
        }
    ]
}

manifests = forge.render(_bundle).manifests

_gateways = [m for m in manifests if m.kind == "Gateway"]
_certs = [m for m in manifests if m.kind == "Certificate"]

# ── Invariants ─────────────────────────────────────────────────────

# Two Gateways rendered, exactly one Certificate (only the TLS one).
assert_gw_count = len(_gateways) == 2
assert_cert_count = len(_certs) == 1

# Gateway with TLS does NOT carry the cert-manager cluster-issuer
# annotation — forge owns issuance via the explicit Certificate, so the
# shim-driving annotation is intentionally absent (one issuance path).
_public_gw = [g for g in _gateways if g.metadata.name == "public"][0]
assert_public_no_annotation = not _public_gw.metadata.get("annotations")

# The HTTPS listener carries the gateway host as its hostname — required
# by cert-manager's gateway-shim and used by the LB for SNI cert select.
_public_https_listener = [l for l in _public_gw.spec.listeners if l.name == "https"][0]
assert_https_listener_hostname = _public_https_listener.hostname == "api.example.com"

# Gateway without TLS has no cert-manager annotation.
_internal_gw = [g for g in _gateways if g.metadata.name == "internal"][0]
assert_internal_no_annotation = not _internal_gw.metadata.get("annotations")

# Certificate shape — apiVersion, naming, issuerRef, secretName, dnsNames.
_cert = _certs[0]
assert_cert_api = _cert.apiVersion == "cert-manager.io/v1"
assert_cert_name = _cert.metadata.name == "public-tls"
assert_cert_namespace = _cert.metadata.namespace == "example-prod"
assert_cert_secret = _cert.spec.secretName == "api-tls"
assert_cert_issuer_name = _cert.spec.issuerRef.name == "letsencrypt-prod"
assert_cert_issuer_kind = _cert.spec.issuerRef.kind == "ClusterIssuer"
assert_cert_issuer_group = _cert.spec.issuerRef.group == "cert-manager.io"
assert_cert_dns = _cert.spec.dnsNames[0] == "api.example.com"

# Forge-management labels propagate to the Certificate.
assert_cert_label_managed = _cert.metadata.labels["app.kubernetes.io/managed-by"] == "forge"
assert_cert_label_partof = _cert.metadata.labels["app.kubernetes.io/part-of"] == "forge-ingress"
