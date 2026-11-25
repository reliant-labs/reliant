"""
Negative test — a password-set code may live at most 7 days (forge#2265).
"""

# expect: must be from 5m to 7d

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identity = forge.ManagedIdentity {roles = [forge.IdentityRole {key = "member"}], code_lifetime = "8d"}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
})
