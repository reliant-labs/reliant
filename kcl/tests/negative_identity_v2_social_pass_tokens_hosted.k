"""
Negative test — `pass_tokens` on a hosted identity. The hosted control plane
does not yet carry it into the login policy it delivers, so the app's
upstream-tokens hook would silently never run: refused until it does.
"""

# expect: set pass_tokens, which works only on forge.IdentityOnHost for now

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identity = forge.ManagedIdentity {
        roles = [forge.IdentityRole {key = "owner"}]
        owners = ["founder@acme.test"]
        methods = []
        social = [
            forge.IdentityGitHub {client_id = "gh-client", client_secret = forge.ManagedSecret {name = "GITHUB_CLIENT_SECRET"}, pass_tokens = True}
        ]
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
})
