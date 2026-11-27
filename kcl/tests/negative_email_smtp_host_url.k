"""
Negative test — EmailOnSMTP.host is a host, not a URL.
"""

# expect: EmailOnSMTP.host 'smtp://smtp.postmarkapp.com:587' must be a host name or an IP address

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_smtp = forge.EmailOnSMTP {host = "smtp.postmarkapp.com", username = forge.ManagedSecret {name = "SMTP_USERNAME"}, password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}

_s = forge.EmailOnSMTP {host = "smtp://smtp.postmarkapp.com:587"}
