"""
Negative test — code_lifetime is <n>m, <n>h or <n>d (forge#2265).
"""

# expect: must be <n>m, <n>h or <n>d

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identity = forge.ManagedIdentity {roles = [forge.IdentityRole {key = "member"}], code_lifetime = "1w"}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnHosted {}}]
})
