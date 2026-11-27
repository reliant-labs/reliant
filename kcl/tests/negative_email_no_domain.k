"""
Negative test — app mail needs a From domain.
"""

# expect: names a forge.Email with no domain, so there is no From address to send app mail as

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

output = forge.render(forge.Bundle {
    project = "acme"
    emails = [forge.Email {name = "mail", runtime = _smtp}]
    workloads = [fw.Workload {name = "api", image = _img, env = forge.email_env("MAIL", "mail"), runtime = forge.OnCluster {target = _k3d}}]
})
