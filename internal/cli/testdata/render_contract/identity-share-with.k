"""
render contract — a PREVIEW env reusing its project's staging users: it declares only applications (derived) and consumers (derived); the owner is named.
"""
# kcl-args: env="preview-142"

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "acme"
    env = "preview-142"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identity = forge.ManagedIdentity {share_with = "staging"}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnHosted {}}]
})
