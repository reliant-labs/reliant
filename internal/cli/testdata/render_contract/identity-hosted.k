"""
render contract — a HOSTED env with a managed identity: the hosted API reads reserved managed secrets, declared members and an SMTP relay are recorded as names, and the only delivered Secret is a machine user's key.
"""
# kcl-args: env="prod"

import forge
import forge.workloads as fw

_ops = forge.ClusterTarget {cluster = "gke_acme_ops", namespace = "acme-ops", connected_cluster = "acme-ops", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    identity = forge.ManagedIdentity {
        roles = [
            forge.IdentityRole {key = "admin", display_name = "Administrators", members_from = "declared", members = ["boss@acme.test"]}
            forge.IdentityRole {key = "member"}
        ]
        machine_users = [forge.IdentityMachineUser {name = "ops", roles = ["admin"], key_secret = "acme-ops-key", cluster = _ops}]
        smtp = forge.IdentitySMTP {host = "smtp.acme.test", user = "postmaster", sender = "no-reply@acme.test", password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnHosted {}}]
})
