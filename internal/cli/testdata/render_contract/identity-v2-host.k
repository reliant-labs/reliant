"""
render contract — identity v2 in the DEV loop: the host-run identity's mail goes to the env's mailpit HostInfra (dev_mail), owners and open signup with magic links, and the login policy is an optional store reference.
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
        forge.HostInfra {name = "mail", engine = "mailpit", port = 1025}
    ]
    identity = forge.ManagedIdentity {
        runtime = forge.IdentityOnHost {infra = "idp"}
        roles = [forge.IdentityRole {key = "owner"}]
        owners = ["dev@acme.test"]
        signup = "open"
        methods = ["password", "passkey", "magic_link"]
        social = [forge.IdentityGoogle {client_id = "dev.apps.googleusercontent.com", client_secret = forge.ManagedSecret {name = "GOOGLE_CLIENT_SECRET"}}]
    }
    workloads = [fw.Workload {
        name = "api", kind = "service"
        runtime = forge.OnHost {runner = "binary", listen_ports = [8080]}
        build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}
        args = ["server"]
    }]
})
