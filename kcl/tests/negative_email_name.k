"""
Negative test — an Email name is an RFC-1123 label of at most 40 characters.
"""

# expect: Email.name 'Mail_Sender' must be an RFC-1123 label of at most 40 characters

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

_e = forge.Email {name = "Mail_Sender", domain = "mail.acme.com", runtime = _smtp}
