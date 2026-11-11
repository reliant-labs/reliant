"""
Positive test — forge.WorkloadURL across runtimes.

A reference resolves by WHERE THE REFERENCING WORKLOAD RUNS, not env-wide
(map §6.3):

  * host / cluster referrer → resolved NOW, at render, to a literal, from
    what the env declares: a host workload's listen port, a route through a
    Gateway listener, a frontend's dev port. The record and the entity carry
    the VALUE, never the reference (pkg/deploy refuses a reference on a
    cluster record).
  * the HOST a cluster referrer dials for a host / compose / frontend
    target is its cluster target's `host_gateway` (host.k3d.internal by
    default): inside a pod, localhost is the pod itself. A host referrer
    dials localhost.
  * hosted referrer → the reference is KEPT as {workloadURL: {name}} in the
    published spec; the control plane resolves it against workloads IT runs
    (hosted → hosted), and re-resolves when a hostname moves.

The illegal direction (hosted → host) is negative_workload_url_hosted_to_host.k.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_pinned = "ghcr.io/acme/web-api@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [
        # host, with a declared port
        fw.Workload {
            name = "api"
            build = forge.GoBuild {cmd = "./cmd/acme"}
            args = ["api"]
            runtime = forge.OnHost {listen_ports = [8085]}
        }
        # cluster, behind an HTTPRoute; it references the host api and the frontend
        fw.Workload {
            name = "admin-api"
            image = "ghcr.io/acme/admin:v1"
            ports = [fw.Port {name = "http", port = 8080}]
            env = {
                API_URL = forge.WorkloadURL {workload = "api"}
                CORS_ORIGINS = forge.WorkloadURL {workload = "web"}
            }
            runtime = forge.OnCluster {target = _k3d}
        }
        # host referrer → cluster target (resolved through the route)
        fw.Workload {
            name = "worker"
            kind = "worker"
            build = forge.GoBuild {cmd = "./cmd/acme"}
            args = ["worker"]
            env = {ADMIN_URL = forge.WorkloadURL {workload = "admin-api"}}
            runtime = forge.OnHost {}
        }
        # hosted referrer → hosted target: kept as a reference
        fw.Workload {
            name = "public-api"
            image = _pinned
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            env = {PEER_URL = forge.WorkloadURL {workload = "public-api-2"}}
            runtime = forge.OnHosted {}
        }
        fw.Workload {
            name = "public-api-2"
            image = _pinned
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            runtime = forge.OnHosted {}
        }
    ]
    gateways = [forge.Gateway {
        name = "public"
        host = "acme.localhost"
        listeners = [forge.GatewayListener {name = "http", port = 8081, protocol = "HTTP"}]
    }]
    http_routes = [forge.HTTPRoute {name = "admin", gateway = "public", listener = "http", service = "admin-api", port = 8080, path = "/admin"}]
    frontends = [forge.Frontend {name = "web", path = "frontends/web", type = "vite", port = 5173, runtime = forge.OnHost {}}]
})

_env = lambda w: str -> {str:} {
    {e.name: e for e in [x for x in output.workloads if x.name == w][0].spec.env}
}
_rec_env = lambda w: str -> {str:} {
    {e.name: e for e in [m for m in output.manifests if m.kind == "Workload" and m.metadata.name == w][0].spec.env}
}

# cluster → host: http://<host_gateway>:<listen port> — a pod's localhost
# is the pod.
assert_cluster_to_host = _env("admin-api")["API_URL"].value == "http://host.k3d.internal:8085"
# cluster → frontend (dev-served on the host): the same gateway.
assert_cluster_to_frontend = _env("admin-api")["CORS_ORIGINS"].value == "http://host.k3d.internal:5173"
# The record carries the value, never the reference.
assert_record_resolved = _rec_env("admin-api")["API_URL"].value == "http://host.k3d.internal:8085"
_rec_entries = [v for _, v in _rec_env("admin-api")]
# Two declared entries plus the derived OTEL_SERVICE_NAME and
# OTEL_RESOURCE_ATTRIBUTES.
assert_record_no_ref = len(_rec_entries) == 4 and all e in _rec_entries { "workloadURL" not in e }
# host → cluster (behind a route): the route URL through its listener.
assert_host_to_routed_cluster = _env("worker")["ADMIN_URL"].value == "http://acme.localhost:8081/admin"
# hosted → hosted: the reference is kept for the control plane.
assert_hosted_keeps_ref = _env("public-api")["PEER_URL"].workloadURL.name == "public-api-2"
assert_hosted_no_value = "value" not in _env("public-api")["PEER_URL"]

# A target that names its own host_gateway (a docker-desktop cluster, a VM)
# is honoured: the gateway is a fact about the CLUSTER, not a forge guess.
_dd = forge.ClusterTarget {cluster = "docker-desktop", namespace = "acme-dev", host_gateway = "host.docker.internal"}
_dd_out = forge.render(forge.Bundle {
    project = "acme"
    workloads = [
        fw.Workload {name = "api", build = forge.GoBuild {cmd = "./cmd/acme"}, args = ["api"], runtime = forge.OnHost {listen_ports = [8085]}}
        fw.Workload {name = "admin-api", image = "localhost:5000/admin", env = {API_URL = forge.WorkloadURL {workload = "api"}}, runtime = forge.OnCluster {target = _dd}}
    ]
})
_dd_env = [w for w in _dd_out.workloads if w.name == "admin-api"][0].spec.env
assert_custom_host_gateway = _dd_env[0].value == "http://host.docker.internal:8085"
