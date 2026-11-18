"""
Negative test — identity v2 V5 — magic links in dev with no mailpit HostInfra.
"""

# expect: offer "magic_link", but the identity can send no mail
# expect: engine 'mailpit'

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
        methods = ["magic_link"]
    }
    workloads = [fw.Workload {name = "api", kind = "service", runtime = forge.OnHost {runner = "binary"}, build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}, args = ["server"]}]
})
