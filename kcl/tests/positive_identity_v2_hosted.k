"""
Positive test — identity v2 sign-in on a HOSTED env: owners, domain signup,
every first-factor method, a pinned passkey RP id, Google and GitHub with the
app's own OAuth clients, and mail addressing. The shape records names and
literals only; the broker's consumer reads the OPTIONAL login policy, and no
workload is handed a social client secret.
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identity = forge.ManagedIdentity {
        roles = [forge.IdentityRole {key = "owner"}, forge.IdentityRole {key = "member"}]
        owners = ["Founder@Acme.test", "cto@acme.test"]
        signup = "domain"
        signup_domains = ["acme.test"]
        signup_role = "member"
        methods = ["password", "passkey", "magic_link"]
        passkey_rp_id = "acme.test"
        social = [
            forge.IdentityGoogle {client_id = "1234.apps.googleusercontent.com", client_secret = forge.ManagedSecret {name = "GOOGLE_CLIENT_SECRET"}}
            forge.IdentityGitHub {client_id = forge.ManagedSecret {name = "GITHUB_CLIENT_ID"}, client_secret = forge.ManagedSecret {name = "GITHUB_CLIENT_SECRET"}}
        ]
        email = forge.IdentityEmail {from_name = "Acme", reply_to = "help@acme.test"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
})

_env = {e.name: e for e in {w.name: w for w in output.workloads}["api"].spec.env}
_id = output.identity
_social = {s.type: s for s in _id.social}

assert_owners_lower_cased_and_sorted = _id.owners == ["cto@acme.test", "founder@acme.test"] and _id.owner_role == "owner"
assert_signup = _id.signup.mode == "domain" and _id.signup.domains == ["acme.test"] and _id.signup.role == "member"
assert_methods = _id.methods == ["password", "passkey", "magic_link"]
assert_passkey_rp_id = _id.passkey_rp_id == "acme.test"
assert_google_literal_client_id = _social["google"].client_id == "1234.apps.googleusercontent.com" and not _social["google"].client_id_secret
assert_google_secret_is_a_name = _social["google"].client_secret_secret == "GOOGLE_CLIENT_SECRET"
assert_google_default_scopes = _social["google"].scopes == ["openid", "profile", "email"]
assert_github_client_id_is_a_name = _social["github"].client_id_secret == "GITHUB_CLIENT_ID" and not _social["github"].client_id
assert_github_default_scopes = _social["github"].scopes == ["read:user", "user:email"]
assert_email_platform_by_default = _id.email.delivery == "platform" and _id.email.from_local == "no-reply" and _id.email.from_name == "Acme" and _id.email.reply_to == "help@acme.test"
assert_no_dev_mail_on_hosted = "dev_mail" not in _id

# THE LOGIN POLICY: the broker's consumer reads it, optionally, from a reserved
# managed secret the control plane writes.
assert_login_policy_is_optional = _env["IDP_LOGIN_POLICY"].managedSecret.name == "RELIANT_IDENTITY_IDENTITY_LOGIN_POLICY" and _env["IDP_LOGIN_POLICY"].managedSecret.optional == True
assert_login_policy_in_the_shape = "RELIANT_IDENTITY_IDENTITY_LOGIN_POLICY" in _id.managed_secrets

# SOCIAL SECRETS NEVER REACH A POD: no env value names them.
_names = ["GOOGLE_CLIENT_SECRET", "GITHUB_CLIENT_SECRET", "GITHUB_CLIENT_ID"]
assert_no_workload_reads_a_social_secret = all k, e in _env { not e.managedSecret or e.managedSecret.name not in _names }
assert_nothing_delivered_to_a_cluster = len(output.control_plane_secrets) == 0
