"""
Negative test — dashboards.row_cap is capped at 10000: a panel renders a handful of series.
"""

# expect: DatabaseDashboards.row_cap 10001 must be between 1 and 10000

import forge

output = forge.render(forge.Bundle {
    project = "demo"
    cluster_target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
    databases = [forge.ManagedDatabase {
        name = "orders"
        dashboards = forge.DatabaseDashboards {enabled = True, row_cap = 10001}
    }]
})
