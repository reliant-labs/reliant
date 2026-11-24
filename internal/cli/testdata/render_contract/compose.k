"""
render contract — the COMPOSE runtime: third-party services the compose file
defines. No build, no image; literal env feeds the compose process env.
"""
# kcl-args: env="dev"

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    workloads = [
        fw.Workload {
            name = "postgres"
            env = {POSTGRES_PORT = "5433"}
            runtime = forge.OnCompose {service = "postgres"}
        }
        fw.Workload {name = "nats", runtime = forge.OnCompose {env_file = ".env.dev"}}
    ]
})
