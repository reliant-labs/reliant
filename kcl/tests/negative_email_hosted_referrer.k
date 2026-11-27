"""
Negative test — a hosted workload cannot send mail yet: the platform opens no SMTP egress for it.
"""

# expect: sending mail from a hosted workload is not yet available on hosted

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    emails = [forge.Email {name = "mail", domain = "mail.acme.com", runtime = _smtp}]
    workloads = [fw.Workload {name = "api", image = _img, env = forge.email_env("MAIL", "mail"), runtime = forge.OnHosted {}}]
})
