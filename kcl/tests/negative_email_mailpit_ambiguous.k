"""
Negative test — with several mailpit HostInfras, EmailOnMailpit.infra names one.
"""

# expect: Declared mailpit infra: [mail-a, mail-b]

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    emails = [forge.Email {name = "mail", domain = "mail.acme.com", runtime = forge.EmailOnMailpit {}}]
    infra = [forge.HostInfra {name = "mail-a", engine = "mailpit", port = 1125}, forge.HostInfra {name = "mail-b", engine = "mailpit", port = 1126}]
})
