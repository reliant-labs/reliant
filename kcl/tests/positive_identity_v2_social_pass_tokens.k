"""
Positive test — identity v2 social sign-in with `pass_tokens`, in the DEV loop
(forge.IdentityOnHost, the only runtime that delivers it today): a provider
declared with it carries it into the shape (beside the scopes it depends on),
and one declared without it carries nothing. It never puts a social client
secret, or anything else, in a workload's env: the tokens reach the app's
login-broker hook server-side, through the login policy entry.
"""
# kcl-args: env="dev"

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "dev"
    lifecycle = "local"
    infra = [
        forge.HostInfra {name = "pg", engine = "postgres", port = 5432}
        forge.HostInfra {name = "idp", engine = "zitadel", port = 8098, idp_database_port = 5432}
    ]
    identity = forge.ManagedIdentity {
        runtime = forge.IdentityOnHost {infra = "idp"}
        roles = [forge.IdentityRole {key = "owner"}]
        owners = ["founder@acme.test"]
        methods = []
        social = [
            forge.IdentityGitHub {client_id = "gh-client", client_secret = forge.ManagedSecret {name = "GITHUB_CLIENT_SECRET"}, scopes = ["read:user", "user:email", "repo"], pass_tokens = True}
            forge.IdentityGoogle {client_id = "1234.apps.googleusercontent.com", client_secret = forge.ManagedSecret {name = "GOOGLE_CLIENT_SECRET"}}
        ]
    }
    workloads = [fw.Workload {name = "api", kind = "service", runtime = forge.OnHost {runner = "binary"}, build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}, args = ["server"]}]
})

_social = {s.type: s for s in output.identity.social}

assert_github_passes_tokens = _social["github"].pass_tokens == True
assert_github_scopes_kept = _social["github"].scopes == ["read:user", "user:email", "repo"]
assert_google_does_not = "pass_tokens" not in _social["google"]
