"""
render contract — ONE env binding every runtime (ADR 0002 §2): host (the
default), compose, cluster, hosted and build-only side by side. The cluster
workload reaches the host api through the cluster's host gateway.
"""
# kcl-args: env="dev"

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
# Declared once, used by whichever workloads run on a runtime that pulls one.
# `api` and `migrate` run OnHost and `cli` is BuildOnly, so they need none.
_image = "localhost:5050/acme"
_build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    control_plane = forge.ControlPlane {endpoint = "http://127.0.0.1:8090"}
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
    workloads = [
        fw.Workload {
            name = "api"
            build = _build
            args = ["api"]
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            runtime = forge.OnHost {runner = "air", air_config = ".air.toml", listen_ports = [8080]}
        }
        fw.Workload {name = "migrate", kind = "job", build = _build, args = ["db", "migrate", "up"], before = [fw.BEFORE_ALL], runtime = forge.OnHost {}}
        fw.Workload {name = "nats", runtime = forge.OnCompose {}}
        fw.Workload {
            name = "search"
            image = _image
            build = _build
            args = ["search"]
            ports = [fw.Port {name = "http", port = 8080}]
            env = {API_URL = forge.WorkloadURL {workload = "api"}}
            runtime = forge.OnCluster {target = _k3d}
        }
        fw.Workload {
            name = "billing"
            kind = "worker"
            image = _image
            build = _build
            args = ["billing"]
            env = {STRIPE_KEY = forge.ManagedSecret {name = "STRIPE_KEY"}}
            runtime = forge.OnHosted {}
        }
        fw.Workload {name = "cli", kind = "tool", build = forge.GoBuild {cmd = "./cmd/acme-cli", output_name = "acme-cli"}, runtime = forge.BuildOnly {}}
    ]
    infra = [forge.HostInfra {name = "postgres", port = 5433, database = "acme"}]
    frontends = [
        forge.Frontend {name = "web", type = "vite", path = "frontends/web", runtime_config = {API_URL = forge.WorkloadURL {workload = "api"}}, runtime = forge.OnHost {}}
        # A frontend on the author's own bucket, beside hosted workloads: the
        # hosted ones do not make it hosted.
        forge.Frontend {
            name = "site"
            type = "vite"
            path = "frontends/site"
            base_path = "/docs"
            cache_control = [forge.CacheRule {pattern = "assets/**", cache_control = "public, max-age=31536000, immutable"}]
            runtime_config = {API_URL = forge.WorkloadURL {workload = "api"}}
            runtime = forge.OnBucket {bucket = "gs://acme-site", cdn = forge.StaticSiteCDN {url_map = "acme-lb"}}
        }
    ]
})
