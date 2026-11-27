"""
Negative test — a forge.EmailRef key is one of the connection facts forge.email_env emits.
"""

# expect: EmailRef.key 'reply_to' must be one of

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

output = forge.render(forge.Bundle {
    project = "acme"
    emails = [forge.Email {name = "mail", domain = "mail.acme.com", runtime = _smtp}]
    workloads = [fw.Workload {name = "api", image = _img, env = {X = forge.EmailRef {name = "mail", key = "reply_to"}}, runtime = forge.OnCluster {target = _k3d}}]
})
