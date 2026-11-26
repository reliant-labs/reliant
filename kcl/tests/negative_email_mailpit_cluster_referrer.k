"""
Negative test — the env's Mailpit listens on 127.0.0.1, so a pod cannot reach it.
"""

# expect: is on forge.EmailOnMailpit, the env's Mailpit, which listens on 127.0.0.1 of this machine, so a workload on the cluster runtime cannot reach it

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
    workloads = [fw.Workload {name = "api", image = _img, env = forge.email_env("MAIL", "mail"), runtime = forge.OnCluster {target = _k3d}}]
})
