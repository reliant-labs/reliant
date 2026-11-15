"""
Negative test — data_access.storage is a closed set: open, audited or deny.
"""

# expect: DataAccess.storage
# expect: storage:read

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    data_access = forge.DataAccess {db_query = "audited", storage = "read-only"}
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
