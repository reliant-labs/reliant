"""
Positive test — a route whose public hostname is a wildcard derives no uptime
check (a prober cannot resolve `*.apps.example.com`; the check could only ever
report down), and `Uptime.skip` may still name it, since earlier forges
derived it.
"""

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
    }, forge.Gateway {
        name = "public"
        host = "app.example.com"
        tls = forge.GatewayTLS {secret_name = "app-tls", cert_issuer = "letsencrypt"}
        listeners = [forge.GatewayListener {name = "https", port = 443, protocol = "HTTPS"}]
    }]
    http_routes = [
        forge.HTTPRoute {name = "apps", gateway = "apps", listener = "https", service = "apps", port = 8080}
        forge.HTTPRoute {name = "billing", gateway = "public", listener = "https", service = "billing", port = 8080, path = "/billing"}
    ]
}

output = forge.render(_b)
assert_no_wildcard_check = [c.name for c in output.uptime_checks] == ["api", "route-billing"]

skipped = forge.render(_b | {uptime = forge.Uptime {skip = ["route-apps"]}})
assert_skip_still_accepted = [c.name for c in skipped.uptime_checks] == ["api", "route-billing"]
