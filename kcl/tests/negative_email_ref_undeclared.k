"""
Negative test — a forge.EmailRef must name an Email the env declares.
"""

# expect: forge.EmailRef names email 'mail', which this environment does not declare

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [fw.Workload {name = "api", image = _img, env = forge.email_env("MAIL", "mail"), runtime = forge.OnCluster {target = _k3d}}]
})
