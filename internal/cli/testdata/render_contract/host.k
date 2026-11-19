"""
render contract — the HOST runtime: an air-run service with a declared
listen port, a go-run migrate job, a host-run postgres, and a dev frontend.
"""
# kcl-args: env="dev"

import forge
import forge.workloads as fw

_build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    workloads = [
        fw.Workload {
            name = "api"
            build = _build
            args = ["api"]
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            env = {
                LOG_LEVEL = "debug"
                API_KEY = forge.SecretRef {name = "acme-secrets", key = "api_key", optional = True}
                WEB_URL = forge.WorkloadURL {workload = "web"}
            }
            runtime = forge.OnHost {runner = "air", air_config = ".air.toml", listen_ports = [8080]}
        }
        fw.Workload {
            name = "migrate"
            kind = "job"
            build = _build
            args = ["db", "migrate", "up"]
            before = [fw.BEFORE_ALL]
            env = {DATABASE_URL = "postgres://postgres:postgres@localhost:5433/acme"}
            runtime = forge.OnHost {}
        }
    ]
    infra = [
        forge.HostInfra {name = "postgres", port = 5433, database = "acme"}
        forge.HostInfra {name = "nats", engine = "nats", port = 4223, nats_monitor_port = 8223}
        forge.HostInfra {name = "temporal", engine = "temporal", port = 7234, temporal_namespaces = ["acme"]}
    ]
    frontends = [forge.Frontend {name = "web", type = "vite", path = "frontends/web", runtime = forge.OnHost {}}]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})
