"""
Positive test — share_with is per identity name: a preview env reuses BOTH of
staging's instances, each the one of the same name there. Each hosted
consumer reads its own identity's reserved managed secrets.
"""

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "preview-142"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identities = [
        forge.ManagedIdentity {
            share_with = "staging"
            applications = [forge.IdentityApplication {name = "console", workload = "admin-api"}]
            consumers = [forge.IdentityConsumer {workload = "admin-api"}]
        }
        forge.ManagedIdentity {
            name = "users"
            share_with = "staging"
            applications = [forge.IdentityApplication {name = "app", workload = "api"}]
            consumers = [forge.IdentityConsumer {workload = "api"}]
        }
    ]
    workloads = [
        fw.Workload {name = "admin-api", image = "ghcr.io/acme/admin", runtime = forge.OnHosted {}}
        fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}
    ]
})

_env = lambda name: str -> {str:} {
    {e.name: e for e in {w.name: w for w in output.workloads}[name].spec.env}
}

assert_both_share = [[i.name, i.share_with] for i in output.identities] == [["identity", "staging"], ["users", "staging"]]
assert_operators_read_their_own = _env("admin-api")["JWT_ISSUER"].managedSecret.name == "RELIANT_IDENTITY_IDENTITY_ISSUER"
assert_users_read_their_own = _env("api")["JWT_ISSUER"].managedSecret.name == "RELIANT_IDENTITY_USERS_ISSUER"
assert_managed_names_per_identity = "RELIANT_IDENTITY_USERS_APP_CLIENT_ID" in output.identities[1].managed_secrets and len([n for n in output.identities[1].managed_secrets if n.startswith("RELIANT_IDENTITY_IDENTITY_")]) == 0
