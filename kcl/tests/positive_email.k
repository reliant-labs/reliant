"""
Positive test — forge.Email, forge.EmailRef and forge.email_env.

An EmailRef lowers by where the Email is delivered and where the referrer
runs:

  1. forge.email_env emits the eight fixed <PREFIX>_<KEY> names, the same in
     every env;
  2. an Email on the env's Mailpit is literals for a host process: 127.0.0.1
     on the mailpit HostInfra's SMTP port, no TLS, no credentials (the one
     `infra` names, or the env's only one);
  3. an Email on forge.EmailOnSMTP carries its declared values, and its
     credentials lower through their own channels: a ManagedSecret is a
     managedSecret, a SecretRef a secretRef on a pod, a literal username a
     value;
  4. the default From: a plain display name goes bare, any other is quoted,
     and no display name is the bare address;
  5. a compose container takes an EmailOnSMTP of literals (a server that
     takes no authentication) in its compose process env;
  6. an env with no emails declares nothing new: Bundle.emails defaults to
     empty.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-acme", namespace = "acme-dev"}
_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
MAIL = forge.Email {name = "mail", domain = "mail.acme.com", from_name = "Acme", from_local = "hello", reply_to = "support@acme.com"}

# 1 + 2. The dev loop: a host process on the env's only Mailpit.
_dev = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    emails = [MAIL | {runtime = forge.EmailOnMailpit {}}]
    infra = [forge.HostInfra {name = "mail", engine = "mailpit", port = 1125}]
    workloads = [fw.Workload {
        name = "api"
        command = ["./bin/api"]
        env = forge.email_env("MAIL", "mail")
        runtime = forge.OnHost {}
    }]
})

# 2. With several Mailpits, `infra` names the one.
_named = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    emails = [MAIL | {runtime = forge.EmailOnMailpit {infra = "mail-b"}}]
    infra = [
        forge.HostInfra {name = "mail-a", engine = "mailpit", port = 1125}
        forge.HostInfra {name = "mail-b", engine = "mailpit", port = 1126}
    ]
    workloads = [fw.Workload {
        name = "api"
        command = ["./bin/api"]
        env = {SMTP_PORT = forge.EmailRef {name = "mail", key = "smtp_port"}}
        runtime = forge.OnHost {}
    }]
})

# 3 + 4. A cluster you run, on an SMTP provider; two senders.
_prod = forge.render(forge.Bundle {
    project = "acme"
    emails = [
        MAIL | {runtime = forge.EmailOnSMTP {
            host = "smtp.postmarkapp.com"
            username = forge.ManagedSecret {name = "SMTP_USERNAME"}
            password = forge.ManagedSecret {name = "SMTP_PASSWORD"}
        }}
        forge.Email {
            name = "billing"
            domain = "billing.acme.com"
            from_name = "Acme, Inc."
            runtime = forge.EmailOnSMTP {
                host = "email-smtp.us-east-1.amazonaws.com"
                port = 465
                tls = "implicit"
                username = "AKIAEXAMPLE"
                password = forge.SecretRef {name = "ses-smtp", key = "password"}
            }
        }
        forge.Email {name = "bare", domain = "acme.com", runtime = forge.EmailOnSMTP {host = "smtp.acme.com"}}
    ]
    workloads = [fw.Workload {
        name = "api"
        image = _img
        env = forge.email_env("MAIL", "mail") | forge.email_env("BILLING", "billing") | {BARE_FROM = forge.EmailRef {name = "bare", key = "from"}}
        runtime = forge.OnCluster {target = _k3d}
    }]
})

# 5. Compose: a relay on this machine with no authentication.
_compose = forge.render(forge.Bundle {
    project = "acme"
    lifecycle = "local"
    emails = [MAIL | {runtime = forge.EmailOnSMTP {host = "host.docker.internal", port = 2525, tls = "none"}}]
    workloads = [fw.Workload {
        name = "mailer"
        kind = "worker"
        env = forge.email_env("MAIL", "mail")
        runtime = forge.OnCompose {}
    }]
})

# 6. No emails.
_none = forge.Bundle {project = "acme"}

_d = {e.name: e for e in _dev.workloads[0].spec.env}
_p = {e.name: e for e in _prod.workloads[0].spec.env}
_c = _compose.workloads[0].runtime.env

# 1. The fixed names.
assert_names = sorted([k for k in forge.email_env("MAIL", "mail")]) == sorted(["MAIL_SMTP_HOST", "MAIL_SMTP_PORT", "MAIL_SMTP_TLS", "MAIL_SMTP_USERNAME", "MAIL_SMTP_PASSWORD", "MAIL_FROM", "MAIL_FROM_DOMAIN", "MAIL_API_URL"]) \
    and sorted([k for k in _d if k.startswith("MAIL_")]) == sorted([k for k in forge.email_env("MAIL", "mail")])

# 2. Mailpit: literals.
assert_mailpit_host = _d.MAIL_SMTP_HOST.value == "127.0.0.1" and _d.MAIL_SMTP_PORT.value == "1125" and _d.MAIL_SMTP_TLS.value == "none"
assert_mailpit_no_auth = not _d.MAIL_SMTP_USERNAME.value and not _d.MAIL_SMTP_PASSWORD.value and not _d.MAIL_SMTP_PASSWORD.managedSecret
assert_mailpit_from = _d.MAIL_FROM.value == "Acme <hello@mail.acme.com>" and _d.MAIL_FROM_DOMAIN.value == "mail.acme.com" and not _d.MAIL_API_URL.value
assert_mailpit_named = {e.name: e for e in _named.workloads[0].spec.env}.SMTP_PORT.value == "1126"

# 3. SMTP: declared values, credentials through their channels.
assert_smtp_host = _p.MAIL_SMTP_HOST.value == "smtp.postmarkapp.com" and _p.MAIL_SMTP_PORT.value == "587" and _p.MAIL_SMTP_TLS.value == "starttls"
assert_smtp_managed = _p.MAIL_SMTP_USERNAME.managedSecret.name == "SMTP_USERNAME" and _p.MAIL_SMTP_PASSWORD.managedSecret.name == "SMTP_PASSWORD"
assert_smtp_implicit = _p.BILLING_SMTP_PORT.value == "465" and _p.BILLING_SMTP_TLS.value == "implicit"
assert_smtp_secret_ref = _p.BILLING_SMTP_USERNAME.value == "AKIAEXAMPLE" and _p.BILLING_SMTP_PASSWORD.secretRef.name == "ses-smtp" and _p.BILLING_SMTP_PASSWORD.secretRef.key == "password"

# 4. The default From.
assert_from_quoted = _p.BILLING_FROM.value == "\"Acme, Inc.\" <no-reply@billing.acme.com>"
assert_from_bare = _p.BARE_FROM.value == "no-reply@acme.com"

# 5. Compose: literals in the compose process env.
assert_compose = _c.MAIL_SMTP_HOST == "host.docker.internal" and _c.MAIL_SMTP_PORT == "2525" and _c.MAIL_SMTP_TLS == "none" and _c.MAIL_SMTP_PASSWORD == "" and _c.MAIL_FROM == "Acme <hello@mail.acme.com>"

# 6. No emails.
assert_none = _none.emails == []
