"""
Negative test — a declared check of a route whose public hostname is a
wildcard: no prober can reach it.
"""

# expect: has a wildcard host

import forge
import forge.workloads as fw

_b = forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
    gateways = [forge.Gateway {
        name = "apps"
        host = "*.apps.example.com"
        tls = forge.GatewayTLS {secret_name = "apps-tls", cert_issuer = "letsencrypt"}
        listeners = [forge.GatewayListener {name = "https", port = 443, protocol = "HTTPS"}]
    }]
    http_routes = [forge.HTTPRoute {name = "apps", gateway = "apps", listener = "https", service = "apps", port = 8080}]
    uptime = forge.Uptime {
        checks = [forge.UptimeCheck {name = "apps", route = "apps"}]
    }
}
output = forge.render(_b)
