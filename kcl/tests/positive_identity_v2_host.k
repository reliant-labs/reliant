"""
Positive test — identity v2 in the DEV loop: a mailpit HostInfra is the
identity's mail, so magic links and open signup are allowed; the env's only
mailpit is found without naming it, and a named one wins when there are
several. The login policy is an optional store reference.
"""
# kcl-args: env="dev"

import forge
import forge.workloads as fw

_infra = [
    forge.HostInfra {name = "pg", engine = "postgres", port = 5432}
    forge.HostInfra {name = "idp", engine = "zitadel", port = 8098, idp_database_port = 5432}
    forge.HostInfra {name = "mail", engine = "mailpit", port = 1025}
]
_api = fw.Workload {name = "api", kind = "service", runtime = forge.OnHost {runner = "binary"}, build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}, args = ["server"]}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    lifecycle = "local"
    infra = _infra
    identity = forge.ManagedIdentity {
        runtime = forge.IdentityOnHost {infra = "idp"}
        roles = [forge.IdentityRole {key = "owner"}]
        owners = ["dev@acme.test"]
        signup = "open"
        methods = ["password", "magic_link", "passkey"]
    }
    workloads = [_api]
})

_env = {e.name: e for e in {w.name: w for w in output.workloads}["api"].spec.env}
_id = output.identity

assert_only_mailpit_is_found = _id.dev_mail == "mail"
assert_login_policy_from_the_store = _env["IDP_LOGIN_POLICY"].secretRef.name == "forge-identity-identity" and _env["IDP_LOGIN_POLICY"].secretRef.key == "LOGIN_POLICY" and _env["IDP_LOGIN_POLICY"].secretRef.optional == True
assert_methods = _id.methods == ["password", "magic_link", "passkey"]
assert_owners = _id.owners == ["dev@acme.test"]
assert_nothing_is_delivered = len(output.control_plane_secrets) == 0

# Two mailpits: the named one.
_two = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    lifecycle = "local"
    infra = _infra + [forge.HostInfra {name = "mail2", engine = "mailpit", port = 1026}]
    identity = forge.ManagedIdentity {
        runtime = forge.IdentityOnHost {infra = "idp", mail = "mail2"}
        methods = ["magic_link"]
    }
    workloads = [_api]
})
assert_named_mailpit_wins = _two.identity.dev_mail == "mail2"

# No v2 field and no mailpit: nothing new is rendered.
_plain = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    lifecycle = "local"
    infra = _infra
    identity = forge.ManagedIdentity {runtime = forge.IdentityOnHost {infra = "idp"}}
    workloads = [_api]
})
assert_dev_mail_recorded_even_without_v2_policy = _plain.identity.dev_mail == "mail"
assert_no_login_policy_without_a_declared_policy = "IDP_LOGIN_POLICY" not in {e.name: e for e in _plain.workloads[0].spec.env}
