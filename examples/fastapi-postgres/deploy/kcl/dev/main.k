"""dev: the app's own docker-compose.yml, as `docker compose up` runs it.

`forge env up dev` brings up every service below through compose, which
builds and runs the containers exactly as before forge: the file stays the
one place their dev settings live. Logs land in .forge/logs/dev/.
"""
import forge
import forge.workloads as fw
import ..workloads as wl

# The compose file owns the container (build, ports, healthcheck, command,
# env), so dev takes only the workload's name from workloads.k.
_compose = lambda w: fw.Workload, service: str -> fw.Workload {
    fw.Workload {name = w.name, runtime = forge.OnCompose {file = "docker-compose.yml", service = service}}
}

_bundle = forge.Bundle {
    project = "fastapi-postgres"
    lifecycle = "local"
    workloads = [
        fw.Workload {name = "db", runtime = forge.OnCompose {file = "docker-compose.yml", service = "db"}}  # staging and prod use a managed Postgres instead
        _compose(wl.migrate, "migrate")
        _compose(wl.api, "api")
    ]
}
output = forge.render(_bundle)
