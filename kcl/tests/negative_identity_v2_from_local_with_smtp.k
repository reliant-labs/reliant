"""
Negative test — identity v2 V13 — email.from_local beside smtp, whose sender is the From.
"""

# expect: with smtp, smtp.sender is the From

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    identity = forge.ManagedIdentity {
        smtp = forge.IdentitySMTP {host = "smtp.acme.test", sender = "no-reply@acme.test", password = forge.ManagedSecret {name = "SMTP_PASSWORD"}}
        email = forge.IdentityEmail {from_local = "hello"}
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
    cluster_target = _prod
})
