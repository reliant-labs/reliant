"""
Negative test — a compose container takes literals only, so a referenced SMTP credential cannot reach it.
"""

# expect: a forge.EmailOnSMTP credential that is a secret reference cannot reach a compose container

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    emails = [forge.Email {name = "mail", domain = "mail.acme.com", runtime = _smtp}]
    workloads = [fw.Workload {name = "mailer", kind = "worker", env = forge.email_env("MAIL", "mail"), runtime = forge.OnCompose {}}]
})
