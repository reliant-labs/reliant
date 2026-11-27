"""
Negative test — a compose container cannot reach the env's Mailpit on 127.0.0.1 either.
"""

# expect: a workload on the compose runtime cannot reach it

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    emails = [forge.Email {name = "mail", domain = "mail.acme.com", runtime = forge.EmailOnMailpit {}}]
    infra = [forge.HostInfra {name = "mail", engine = "mailpit", port = 1125}]
    workloads = [fw.Workload {name = "mailer", kind = "worker", env = forge.email_env("MAIL", "mail"), runtime = forge.OnCompose {}}]
})
