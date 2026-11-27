"""
Positive test — a declared status page resolves into the release shape's
`status_page`: components in declaration order, each naming derived and
declared uptime checks (sorted), optional fields left out when unset, and no
visibility field (publishing is control-plane state, never declared).
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api:v1"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
    frontends = [forge.Frontend {
        name = "site"
        image = "ghcr.io/acme/site"
        path = "site"
        type = "vite"
        public_dir = "dist"
        runtime = forge.OnHosted {}
    }]
    gateways = [forge.Gateway {
        name = "public"
        host = "app.example.com"
        tls = forge.GatewayTLS {secret_name = "app-tls", cert_issuer = "letsencrypt"}
        listeners = [forge.GatewayListener {name = "https", port = 443, protocol = "HTTPS"}]
    }]
    http_routes = [forge.HTTPRoute {name = "billing", gateway = "public", listener = "https", service = "billing", port = 8080, path = "/billing"}]
    uptime = forge.Uptime {
        checks = [forge.UptimeCheck {name = "api-ready", target = "api", path = "/readyz"}]
    }
    status_page = forge.StatusPage {
        title = "Acme status"
        logo_url = "https://acme.example.com/logo.svg"
        accent_color = "#5B21B6"
        links = [forge.StatusLink {label = "Support", url = "https://acme.example.com/help"}]
        components = [
            forge.StatusComponent {name = "Website", checks = ["site"]}
            forge.StatusComponent {name = "API", checks = ["route-billing", "api-ready", "api"], description = "REST and webhooks"}
        ]
    }
})

_p = output.status_page

assert_title = _p.title == "Acme status"
assert_presentation = _p.logo_url == "https://acme.example.com/logo.svg" and _p.accent_color == "#5B21B6"
assert_links = _p.links == [{label = "Support", url = "https://acme.example.com/help"}]
assert_component_order = [c.name for c in _p.components] == ["Website", "API"]
assert_checks_sorted = _p.components[1].checks == ["api", "api-ready", "route-billing"]
assert_description = _p.components[1].description == "REST and webhooks" and "description" not in _p.components[0]
assert_no_visibility = "public" not in _p and "visibility" not in _p
assert_checks_still_rendered = [c.name for c in output.uptime_checks] == ["api", "api-ready", "route-billing", "site"]
