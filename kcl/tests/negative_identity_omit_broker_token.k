"""
Negative test — omitting the broker token of a native-broker consumer.
"""

# expect: cannot be omitted

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
        consumers = [forge.IdentityConsumer {workload = "api", omit = ["broker_token"]}]
    }
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _prod}}]
})
