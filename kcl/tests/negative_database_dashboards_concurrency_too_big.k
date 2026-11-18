"""
Negative test — max_concurrency is capped at 4: each query holds a connection.
"""

# expect: DatabaseDashboards.max_concurrency 5 must be between 1 and 4

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
    databases = [forge.ManagedDatabase {
        name = "orders"
        dashboards = forge.DatabaseDashboards {enabled = True, max_concurrency = 5}
    }]
})
