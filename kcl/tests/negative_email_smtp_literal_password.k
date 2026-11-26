"""
Negative test — an SMTP password is a reference, never a committed literal.
"""

# expect: EmailOnSMTP.password is a literal: a password in a committed file is a leaked password

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

_s = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = "user", password = "hunter2"}
