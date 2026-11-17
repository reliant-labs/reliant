"""
Negative test — identity v2: a workload's env reads the store name a social
provider's client secret lives under. Only the identity reconciler reads it.
"""

# expect: read a secret that Bundle.identity.social names as an OAuth client secret

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identity = forge.ManagedIdentity {
        social = [forge.IdentityGoogle {client_id = "1.apps.googleusercontent.com", client_secret = forge.ManagedSecret {name = "GOOGLE_CLIENT_SECRET"}}]
    }
    workloads = [fw.Workload {
        name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}
        env = {GOOGLE_CLIENT_SECRET = forge.ManagedSecret {name = "GOOGLE_CLIENT_SECRET"}}
    }]
})
