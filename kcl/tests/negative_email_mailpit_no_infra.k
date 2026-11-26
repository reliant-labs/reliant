"""
Negative test — an Email on forge.EmailOnMailpit needs a mailpit HostInfra.
"""

# expect: are on forge.EmailOnMailpit, which needs a forge.HostInfra with engine 'mailpit'

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

output = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    emails = [forge.Email {name = "mail", domain = "mail.acme.com", runtime = forge.EmailOnMailpit {}}]
})
