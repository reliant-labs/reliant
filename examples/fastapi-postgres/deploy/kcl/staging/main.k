"""staging: on Reliant hosting, with a managed Postgres.

Each workload in workloads.k runs on the platform (forge.OnHosted), built
once and promoted between envs by digest. Credentials come from the
platform's write-only store: `forge secret set --env staging <NAME>` (the
value on stdin). Hosted workloads and databases need billing on your
organization; `forge env deploy` prints the link when it is missing.
"""
import forge
import forge.workloads as fw
import ..workloads as wl

# Its name may not be a workload's. RETAIN: deleting it never drops the data.
_db = forge.ManagedDatabase {
    name = "notes"
    runtime = forge.OnHosted {}
}
_database_url = forge.DatabaseRef {name = _db.name}

_hosted = lambda w: fw.Workload -> fw.Workload {
    w | {runtime = forge.OnHosted {}}
}

_bundle = forge.Bundle {
    project = "fastapi-postgres"
    control_plane = forge.ControlPlane {}
    workloads = [
        _hosted(wl.migrate) | {env.DATABASE_URL = _database_url}
        _hosted(wl.api) | {env.DATABASE_URL = _database_url}
    ]
    databases = [_db]
    secret_provider = forge.HostedSecrets {}
}
output = forge.render(_bundle)
