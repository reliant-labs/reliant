"""
Negative test — plaintext SMTP is accepted only for a server on this machine.
"""

# expect: EmailOnSMTP.tls = "none" sends credentials and mail in plaintext

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

_s = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", tls = "none", username = "user", password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}
