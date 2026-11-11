"""
render contract — the HOSTED runtime: a service and a migrate job published
as Workload CRs, a hosted database, and a frontend on forge.OnHosted.
References stay references: the control plane resolves them.
"""
# kcl-args: env="prod"

import forge
import forge.workloads as fw

_build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}
# A hosted env has no registry either: the workload declares its reference,
# and forge's hosted publish pins it to repository@digest.
_image = "ghcr.io/acme/acme"

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    workloads = [
        fw.Workload {
            name = "api"
            image = _image
            build = _build
            args = ["api"]
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            env = {
                LOG_LEVEL = "info"
                API_KEY = forge.SecretRef {name = "acme-secrets", key = "api_key", store_key = "API_KEY", optional = True}
                DATABASE_URL = forge.DatabaseRef {name = "db"}
                CORS_ORIGINS = forge.WorkloadURL {workload = "web"}
            }
            runtime = forge.OnHosted {}
        }
        fw.Workload {
            name = "migrate"
            kind = "job"
            image = _image
            build = _build
            args = ["db", "migrate", "up"]
            before = [fw.BEFORE_ALL]
            env = {DATABASE_URL = forge.DatabaseRef {name = "db"}}
            runtime = forge.OnHosted {}
        }
    ]
    databases = [forge.ManagedDatabase {name = "db", runtime = forge.OnHosted {}}]
    frontends = [forge.Frontend {
        name = "web"
        image = "ghcr.io/acme/web"
        type = "vite"
        path = "frontends/web"
        runtime_config = {API_URL = forge.WorkloadURL {workload = "api"}}
        public_dir = "dist"
        runtime = forge.OnHosted {}
    }]
})
