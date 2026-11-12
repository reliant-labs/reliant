"""
Positive test — OpenBao.public (SF8). The declared Gateway gains an HTTPS
listener for the public host with a certificate of its own (the Gateway's shared
certificate is not re-issued), the record carries the endpoint's settings, and
the audience of every cluster hangs off the public host. A Gateway that already
serves the host is used as it is.
"""

# kcl-args: env=prod

import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

_gateway = forge.Gateway {
    name = "public"
    tls = forge.GatewayTLS {cert_issuer = "letsencrypt-dns01", secret_name = "public-tls"}
    listeners = [
        forge.GatewayListener {name = "http", port = 80, protocol = "HTTP"}
        forge.GatewayListener {name = "https-api", port = 443, protocol = "HTTPS", hostname = "api.example.com"}
    ]
}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _hub
    gateways = [_gateway]
    openbao = forge.OpenBao {
        public = forge.OpenBaoPublic {host = "secrets.example.com", gateway = "public", requests_per_second = 300, per_source_requests_per_second = 30}
    }
})

_ob = [m for m in output.manifests if m.kind == "OpenBao"][0]
_gw = [m for m in output.manifests if m.kind == "Gateway" and m.metadata.name == "public"][0]
_certs = [m for m in output.manifests if m.kind == "Certificate"]
_listener = [l for l in _gw.spec.listeners if l.hostname == "secrets.example.com"]

assert_listener_added = len(_gw.spec.listeners) == 3 and len(_listener) == 1 and _listener[0].name == "openbao-public" and _listener[0].port == 443 and _listener[0].protocol == "HTTPS"
assert_listener_terminates_tls = _listener[0].tls.mode == "Terminate" and _listener[0].tls.certificateRefs[0].name == "openbao-public-tls"
assert_own_certificate = len([c for c in _certs if c.spec.secretName == "openbao-public-tls"]) == 1 and [c for c in _certs if c.spec.secretName == "openbao-public-tls"][0].spec.dnsNames == ["secrets.example.com"]
assert_shared_certificate_untouched = [c for c in _certs if c.spec.secretName == "public-tls"][0].spec.dnsNames == ["api.example.com"]
assert_record_public = _ob.spec.public.host == "secrets.example.com" and _ob.spec.public.gateway == "public" and _ob.spec.public.gatewayNamespace == "acme-prod" and _ob.spec.public.listener == "openbao-public"
assert_record_limits = _ob.spec.public.requestsPerSecond == 300 and _ob.spec.public.perSourceRequestsPerSecond == 30 and _ob.spec.public.maxConnections == 2000 and _ob.spec.public.requestTimeout == "30s"
assert_record_port = _ob.spec.public.port == 8210 and _ob.spec.public.proxyNamespace == "envoy-gateway-system" and len(_ob.spec.public.trustedProxyCIDRs) == 5
assert_audience_is_the_public_host = output.openbao.audience == "https://secrets.example.com/c/hub"
assert_no_unrendered_warning = len([w for w in output.openbao.warnings if "not rendered" in w]) == 0
assert_gateway_reported = len([g for g in output.gateways if g.name == "public"][0].listeners) == 3
