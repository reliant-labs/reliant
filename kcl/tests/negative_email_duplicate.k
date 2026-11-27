"""
Negative test — an Email name is declared once per env: forge.EmailRef names one.
"""

# expect: Bundle.emails: an email name is declared twice

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

output = forge.render(forge.Bundle {
    project = "acme"
    emails = [forge.Email {name = "mail", domain = "mail.acme.com", runtime = _smtp}, forge.Email {name = "mail", domain = "other.acme.com", runtime = _smtp}]
})
