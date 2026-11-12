"""
render contract — the DEV loop: the identity runs on the env's host-run Zitadel, a seed user exists, and the host API's typed config is read from the env's secret store.
"""
# kcl-args: env="dev"

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    lifecycle = "local"
    infra = [
        forge.HostInfra {name = "pg", engine = "postgres", port = 5432}
        forge.HostInfra {name = "idp", engine = "zitadel", port = 8098, idp_database_port = 5432}
    ]
    identity = forge.ManagedIdentity {
        runtime = forge.IdentityOnHost {infra = "idp"}
        roles = [forge.IdentityRole {key = "admin"}]
        seed_users = [forge.IdentitySeedUser {email = "dev@acme.test", roles = ["admin"], password_secret = forge.ManagedSecret {name = "DEV_PASSWORD"}}]
    }
    workloads = [fw.Workload {
        name = "api", kind = "service"
        runtime = forge.OnHost {runner = "binary", listen_ports = [8080]}
        build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}
        args = ["server"]
    }]
})
