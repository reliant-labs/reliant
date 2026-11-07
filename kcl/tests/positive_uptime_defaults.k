"""
Positive test — a hosted env is probed with NO declaration: forge derives one
uptime check per hosted frontend, per hosted workload exposing a port, and per
HTTP route a hostname names.

  * a hosted frontend and an exposed hosted workload are targets the control
    plane resolves (the platform allocates their hostnames), GET /, 60s/10s;
  * a workload exposing no port is not public and gets no check;
  * a route with a hostname is checked at its public URL and any answer below
    500 is up (its own path may 404 or ask for a login);
  * a route no hostname names gets no check (localhost is not public);
  * every check is stamped source=default and the list is sorted by name.
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "fixture"
    control_plane = forge.ControlPlane {}
    workloads = [
        fw.Workload {
            name = "api"
            image = "ghcr.io/acme/api:v1"
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            runtime = forge.OnHosted {}
        }
        fw.Workload {
            name = "worker"
            image = "ghcr.io/acme/worker:v1"
            ports = [fw.Port {name = "http", port = 9090}]
            runtime = forge.OnHosted {}
        }
    ]
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
    }, forge.Gateway {
        name = "internal"
        listeners = [forge.GatewayListener {name = "http", port = 80, protocol = "HTTP"}]
    }]
    http_routes = [
        forge.HTTPRoute {name = "billing", gateway = "public", listener = "https", service = "billing", port = 8080, path = "/billing"}
        forge.HTTPRoute {name = "hidden", gateway = "internal", listener = "http", service = "hidden", port = 8080}
    ]
})

_by = {c.name: c for c in output.uptime_checks}

assert_names_sorted = [c.name for c in output.uptime_checks] == ["api", "route-billing", "site"]
assert_frontend = _by.site.target == "site" and _by.site.method == "GET" and _by.site.interval_seconds == 60 and _by.site.timeout_seconds == 10 and _by.site.source == "default"
assert_workload = _by.api.target == "api" and _by.api.source == "default"
assert_unexposed_workload_unchecked = "worker" not in _by
assert_route_url = _by["route-billing"].url == "https://app.example.com/billing"
assert_route_below_500_is_up = _by["route-billing"].expect_status == ["2xx", "3xx", "4xx"]
assert_hostless_route_unchecked = "route-hidden" not in _by
