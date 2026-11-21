"""
Positive test — forge.WorkloadURL from a DEV-SERVED frontend resolves at
RENDER time, from what the env declares:

  * a HOST workload → http://localhost:<its declared listen port>;
  * a CLUSTER workload behind an HTTPRoute → the route's URL through its
    Gateway listener (scheme, host, non-default port, path);
  * literals pass through, including an explicit empty string.

The declared shape rides along too (references kept in runtime_config_spec)
so tooling can show what was authored.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}

_out = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    workloads = [
        fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/acme"}, args = ["api"], runtime = forge.OnHost {listen_ports = [8085]}}
        fw.Workload {name = "admin-api", image = "ghcr.io/acme/admin:v1", ports = [fw.Port {name = "http", port = 8080}], runtime = forge.OnCluster {target = _k3d}}
    ]
    gateways = [forge.Gateway {
        name = "public"
        host = "acme.localhost"
        listeners = [forge.GatewayListener {name = "http", port = 8081, protocol = "HTTP"}]
    }]
    http_routes = [forge.HTTPRoute {name = "admin", gateway = "public", listener = "http", service = "admin-api", port = 8080, path = "/admin"}]
    frontends = [forge.Frontend {
        name = "web"
        path = "frontends/web"
        type = "vite"
        port = 5173
        runtime = forge.OnHost {}
        runtime_config = {
            API_URL = forge.WorkloadURL {workload = "api"}
            ADMIN_URL = forge.WorkloadURL {workload = "admin-api"}
            APP_NAME = "acme"
            FEATURE_X = ""
        }
    }]
})

_fe = _out.frontends[0]
assert_api_url = _fe.runtime_config.API_URL == "http://localhost:8085"
assert_admin_url = _fe.runtime_config.ADMIN_URL == "http://acme.localhost:8081/admin"
assert_literal = _fe.runtime_config.APP_NAME == "acme"
assert_empty_literal = _fe.runtime_config.FEATURE_X == ""
assert_spec_ref_kept = _fe.runtime_config_spec.API_URL.workloadURL.name == "api"
