"""
Negative test — a timeout in minutes parses but exceeds the 10s ceiling.
"""

# expect: must be between "1s" and "10s"

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
    databases = [forge.ManagedDatabase {
        name = "orders"
        dashboards = forge.DatabaseDashboards {enabled = True, timeout = "1m"}
    }]
})
