"""
Negative test — the display name is a mail header: no CR, LF, angle brackets or quotes.
"""

# expect: Email 'mail'.from_name must be at most 64 characters with no CR, LF

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

_e = forge.Email {name = "mail", domain = "mail.acme.com", from_name = "Acme <evil@example.com>", runtime = _smtp}
