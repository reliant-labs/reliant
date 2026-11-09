"""
Positive test — declared uptime checks resolve fully: a url as written, a
route as its public URL, a target with its path; every default written out so
the recorded shape is the whole truth. A declared check with a derived one's
name replaces it.
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
        checks = [
            forge.UptimeCheck {name = "shop", url = "https://shop.example.com/healthz", interval = "30s", timeout = "5s", expect_status = ["200", "2xx"], expect_body = "ok"}
            forge.UptimeCheck {name = "billing-health", route = "billing"}
            forge.UptimeCheck {name = "api-ready", target = "api", path = "/readyz", method = "HEAD"}
            forge.UptimeCheck {name = "site", target = "site", path = "/", interval = "2m"}
        ]
    }
})

_by = {c.name: c for c in output.uptime_checks}

assert_sorted = [c.name for c in output.uptime_checks] == ["api", "api-ready", "billing-health", "route-billing", "shop", "site"]
assert_url_as_written = _by.shop.url == "https://shop.example.com/healthz" and _by.shop.source == "user"
assert_schedule_resolved = _by.shop.interval_seconds == 30 and _by.shop.timeout_seconds == 5
assert_statuses_sorted = _by.shop.expect_status == ["200", "2xx"]
assert_body = _by.shop.expect_body == "ok"
assert_route_resolved = _by["billing-health"].url == "https://app.example.com/billing" and "target" not in _by["billing-health"]
assert_route_defaults_written = _by["billing-health"].method == "GET" and _by["billing-health"].interval_seconds == 60 and _by["billing-health"].timeout_seconds == 10
assert_target_path = _by["api-ready"].target == "api" and _by["api-ready"].path == "/readyz" and _by["api-ready"].method == "HEAD"
assert_declared_replaces_derived = _by.site.source == "user" and _by.site.interval_seconds == 120 and "path" not in _by.site
assert_derived_kept = _by.api.source == "default" and _by["route-billing"].source == "default"
