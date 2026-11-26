"""prod: on Reliant hosting, with a managed Postgres.

Each workload in workloads.k runs on the platform (forge.OnHosted), built
once and promoted between envs by digest. Credentials come from the
platform's write-only store: `forge secret set --env prod <NAME>` (the
value on stdin). Hosted workloads and databases need billing on your
organization; `forge env deploy` prints the link when it is missing.

ANONYMOUS, AS SCAFFOLDED (--claimable): `forge env deploy prod --claimable`
publishes with no account and prints a claim link for a human. An unclaimed
deploy is one service with its migration job and one database, each sized
to the plan (125m / 128 MiB), takes no secret values, and has NO outbound
internet: DNS and its own Postgres only. Once it is claimed, delete
`claimable = True`, the `resources` line and the database's `spec`.
"""
import forge
import forge.workloads as fw
import ..workloads as wl

# Its name may not be a workload's. RETAIN: deleting it never drops the data.
_db = forge.ManagedDatabase {
    name = "notes"
    runtime = forge.OnHosted {}
    spec = {storageGiB = 1}
}
_database_url = forge.DatabaseRef {name = _db.name}

# The claimable plan's largest shape, request and limit, no burst.
_claimable_resources = fw.Resources {
    cpuRequestMillicores = 125
    cpuLimitMillicores = 125
    memoryRequestBytes = forge.mib(128)
    memoryLimitBytes = forge.mib(128)
}

_hosted = lambda w: fw.Workload -> fw.Workload {
    w | {
        runtime = forge.OnHosted {}
        resources = _claimable_resources
    }
}

_bundle = forge.Bundle {
    project = "fastapi-postgres"
    control_plane = forge.ControlPlane {claimable = True}
    workloads = [
        _hosted(wl.migrate) | {env.DATABASE_URL = _database_url}
        _hosted(wl.api) | {env.DATABASE_URL = _database_url}
    ]
    databases = [_db]
    secret_provider = forge.HostedSecrets {}
}
output = forge.render(_bundle)
