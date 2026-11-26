"""dev: the app as a host process, beside a host-native Postgres.

`forge env up dev` runs each workload's own dev command on this machine
(install the app's dependencies first), with PORT set to a free port and
DATABASE_URL to the Postgres forge runs for it.
Logs land in .forge/logs/dev/.
"""
import forge
import forge.workloads as fw
import kcl_plugin.forge as plugin
import ..workloads as wl

_pg_port = plugin.resolve_port("express-postgres-dev-postgres", 5432)
_database_url = "postgres://postgres:postgres@localhost:${_pg_port}/express-postgres?sslmode=disable"

_on_host = lambda w: fw.Workload, command: [str], port: int, cwd: str -> fw.Workload {
    w | {
        command = command
        env = w.env | ({PORT = str(port)} if port else {})
        runtime = forge.OnHost {listen_ports = [port] if port else [], working_dir = cwd}
    }
}

_api_port = plugin.resolve_port("express-postgres-dev-api", 3000)

_bundle = forge.Bundle {
    project = "express-postgres"
    lifecycle = "local"
    workloads = [
        _on_host(wl.migrate, ["npm", "run", "migrate"], 0, ".") | {env.DATABASE_URL = _database_url}  # check: package.json script "migrate"
        _on_host(wl.api, ["npm", "start"], _api_port, ".") | {env.DATABASE_URL = _database_url}  # check: dev runs package.json script "start"
    ]
    infra = [forge.HostInfra {name = "postgres", port = _pg_port, database = "express-postgres"}]
    # Dev credentials: the gitignored secrets/dev.yaml (`NAME: value`).
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
}
output = forge.render(_bundle)
