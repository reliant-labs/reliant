"""
render contract — identity v2 on a HOSTED env with every hosted v2 field: owners, domain signup, all three methods, a passkey RP id, Google (literal client id) and GitHub (secret client id), and mail addressing. A hosted and a cluster consumer read the OPTIONAL login policy; no workload env names a social secret.
"""
# kcl-args: env="prod"

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    cluster_target = _prod
    identity = forge.ManagedIdentity {
        roles = [forge.IdentityRole {key = "owner"}, forge.IdentityRole {key = "member"}]
        applications = [
            forge.IdentityApplication {name = "prod:api", workload = "api"}
            forge.IdentityApplication {name = "prod:admin", workload = "admin"}
        ]
        owners = ["Founder@Acme.test"]
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
    workloads = [
        fw.Workload {name = "api", image = "ghcr.io/acme/api", ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnHosted {}}
        fw.Workload {name = "admin", image = "ghcr.io/acme/admin", runtime = forge.OnCluster {target = _prod}}
    ]
})
