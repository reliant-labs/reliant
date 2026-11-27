"""
render contract — forge.Email: one sender handed to workloads through
forge.email_env, lowered by where it is delivered. A host process reads the
env's Mailpit as literals; a cluster workload reads a second sender on an
SMTP provider, its credentials as managedSecret / secretRef channels and a
literal username. Nothing new reaches the top level: an Email is env values
on the workloads that read it.
"""
# kcl-args: env="dev"

import forge
import forge.workloads as fw

_build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    lifecycle = "local"
    emails = [
        forge.Email {name = "mail", domain = "mail.acme.com", from_name = "Acme", from_local = "hello", runtime = forge.EmailOnMailpit {}}
        forge.Email {
            name = "billing"
            domain = "billing.acme.com"
            from_name = "Acme, Inc."
            runtime = forge.EmailOnSMTP {
                host = "smtp.postmarkapp.com"
                username = forge.ManagedSecret {name = "SMTP_USERNAME"}
                password = forge.SecretRef {name = "postmark-smtp", key = "password"}
            }
        }
    ]
    workloads = [
        fw.Workload {
            name = "api"
            build = _build
            args = ["api"]
            env = forge.email_env("MAIL", "mail")
            runtime = forge.OnHost {}
        }
        fw.Workload {
            name = "invoicer"
            kind = "worker"
            build = _build
            image = "localhost:5051/acme"
            args = ["invoicer"]
            env = forge.email_env("MAIL", "billing")
            runtime = forge.OnCluster {target = _k3d}
        }
    ]
    infra = [forge.HostInfra {name = "mail", engine = "mailpit", port = 1125}]
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
})
