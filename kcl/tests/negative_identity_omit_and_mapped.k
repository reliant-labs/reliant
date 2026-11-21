"""
Negative test — an output both mapped in fields and omitted.
"""

# expect: both mapped in fields and listed in omit

import forge
import forge.workloads as fw

_prod = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", connected_cluster = "acme-prod", platform = "amd64"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {}
    cluster_target = _prod
    identity = forge.ManagedIdentity {
        applications = [forge.IdentityApplication {name = "console", workload = "api", kind = "native_broker"}]
        consumers = [forge.IdentityConsumer {workload = "api", fields = forge.IdentityFields {scopes = "my_scopes"}, omit = ["scopes"]}]
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
