"""
Negative test — two identities on the env's host-run IdP.
"""

# expect: Bundle.identities [identity, users] all run on forge.IdentityOnHost
# expect: Run at most one identity of this env on the host IdP

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
    identities = [
        forge.ManagedIdentity {runtime = forge.IdentityOnHost {infra = "idp"}, applications = [forge.IdentityApplication {name = "console", workload = "admin-api"}], consumers = [forge.IdentityConsumer {workload = "admin-api"}]}
        forge.ManagedIdentity {name = "users", runtime = forge.IdentityOnHost {infra = "idp"}, applications = [forge.IdentityApplication {name = "app", workload = "api"}], consumers = [forge.IdentityConsumer {workload = "api"}]}
    ]
    workloads = [
        fw.Workload {name = "admin-api", kind = "service", runtime = forge.OnHost {runner = "binary"}, build = forge.GoBuild {cmd = "./cmd/admin", output_name = "admin"}, args = ["server"]}
        fw.Workload {name = "api", kind = "service", runtime = forge.OnHost {runner = "binary"}, build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}, args = ["server"]}
    ]
})
